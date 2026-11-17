package wire

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const (
	writeTimeout   = 15 * time.Second
	maxRemovedPerF = 2000
	statesMinGap   = 50 * time.Millisecond
)

type inMsg struct {
	kind    protocol.FrameKind
	channel uint32
	seq     uint32
	payload []byte
	err     error
}

type connSub struct {
	qid    uint32
	sub    *view.Sub
	last   time.Time
	urgent bool // client-initiated change: bypass the tick once
}

type conn struct {
	s   *Server
	ws  *websocket.Conn
	in  chan inMsg
	seq uint32

	subs     map[uint32]*connSub
	unacked  map[uint32]int
	inflight int

	d       *dict
	buf     bytes.Buffer
	enc     *msgpack.Encoder
	rows    bytes.Buffer
	rowsEnc *msgpack.Encoder

	timer     *time.Timer
	deadline  time.Time
	lastState time.Time
	statesDue bool

	ctx context.Context
}

func newConn(s *Server, ws *websocket.Conn) *conn {
	c := &conn{s: s, ws: ws, in: make(chan inMsg, 16), subs: map[uint32]*connSub{}, unacked: map[uint32]int{}}
	c.initCodec()
	ws.SetReadLimit(1 << 20)
	return c
}

func (c *conn) initCodec() {
	c.d = newDict()
	c.enc = msgpack.NewEncoder(&c.buf)
	c.enc.UseCompactInts(true)
	c.rowsEnc = msgpack.NewEncoder(&c.rows)
	c.rowsEnc.UseCompactInts(true)
}

func (c *conn) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.ctx = ctx
	defer c.ws.CloseNow()
	defer func() {
		for _, cs := range c.subs {
			cs.sub.Close()
		}
	}()

	go c.readLoop(ctx, cancel)

	c.timer = time.NewTimer(time.Hour)
	c.timer.Stop()
	defer c.timer.Stop()
	engCh, stopEng := c.s.opt.Engine.Listen()
	defer stopEng()
	clCh, stopCl := c.s.opt.Manager.Listen()
	defer stopCl()

	hello := protocol.Hello{Version: protocol.Version, AgentVer: c.s.opt.AgentVersion, Mode: c.s.opt.Mode}
	if c.send(protocol.KindHello, 0, hello) != nil {
		return
	}
	c.statesDue = true
	c.sendStates()

	for {
		select {
		case <-ctx.Done():
			c.ws.Close(websocket.StatusNormalClosure, "")
			return
		case m := <-c.in:
			if m.err != nil {
				return
			}
			if c.handle(m) != nil {
				return
			}
			if c.flushDirty() != nil {
				return
			}
		case <-engCh:
			if c.flushDirty() != nil {
				return
			}
		case <-clCh:
			c.statesDue = true
			if c.sendStates() != nil {
				return
			}
		case <-c.timer.C:
			c.deadline = time.Time{}
			if c.sendStates() != nil || c.flushDirty() != nil {
				return
			}
		}
	}
}

func (c *conn) readLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			select {
			case c.in <- inMsg{err: err}:
			case <-ctx.Done():
			}
			return
		}
		if typ != websocket.MessageBinary {
			c.ws.Close(websocket.StatusUnsupportedData, "binary frames only")
			return
		}
		kind, ch, seq, payload, err := ParseFrame(b)
		if err != nil {
			c.ws.Close(websocket.StatusProtocolError, "short frame")
			return
		}
		select {
		case c.in <- inMsg{kind: kind, channel: ch, seq: seq, payload: payload}:
		case <-ctx.Done():
			return
		}
	}
}

// arm schedules a wake-up no later than t.
func (c *conn) arm(t time.Time) {
	if !c.deadline.IsZero() && !t.Before(c.deadline) {
		return
	}
	c.timer.Reset(max(time.Until(t), time.Millisecond))
	c.deadline = t
}

// ---- sending --------------------------------------------------------------------------

// writeFrame sends c.buf (header placeholder already reserved) with the next seq.
func (c *conn) writeFrame(kind protocol.FrameKind, channel uint32) (uint32, int, error) {
	c.seq++
	b := c.buf.Bytes()
	PutHeader(b, kind, channel, c.seq)
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		return 0, 0, err
	}
	return c.seq, len(b), nil
}

func (c *conn) begin() {
	c.buf.Reset()
	c.buf.Write(make([]byte, HeaderLen))
}

// send encodes payload as msgpack and sends it as one frame.
func (c *conn) send(kind protocol.FrameKind, channel uint32, payload any) error {
	c.begin()
	if err := c.enc.Encode(payload); err != nil {
		return err
	}
	_, _, err := c.writeFrame(kind, channel)
	return err
}

func (c *conn) flushDict() (sent bool, err error) {
	for {
		add, ok := c.d.take(protocol.MaxFrameBytes - 1024)
		if !ok {
			return sent, nil
		}
		sent = true
		if err := c.send(protocol.KindDict, 0, add); err != nil {
			return sent, err
		}
	}
}

func (c *conn) sendStates() error {
	if !c.statesDue {
		return nil
	}
	if since := time.Since(c.lastState); since < statesMinGap {
		c.arm(c.lastState.Add(statesMinGap))
		return nil
	}
	c.statesDue = false
	c.lastState = time.Now()
	return c.send(protocol.KindClusterState, 0, c.s.opt.Manager.States())
}

func (c *conn) canSend() bool {
	return len(c.unacked) < protocol.MaxUnacked && c.inflight < protocol.MaxInFlightBytes
}

// flushDirty sends patches for dirty subscriptions, at most one per tick each; subscriptions
// blocked by backpressure stay dirty and are retried on the next ack.
func (c *conn) flushDirty() error {
	now := time.Now()
	for _, cs := range c.subs {
		if !cs.sub.Dirty() {
			continue
		}
		if next := cs.last.Add(c.s.opt.Tick); !cs.urgent && now.Before(next) {
			c.arm(next)
			continue
		}
		if !c.canSend() {
			return nil
		}
		if err := c.sendSub(cs); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) sendSub(cs *connSub) error {
	p := cs.sub.Prepare()
	if p == nil {
		cs.urgent = false
		return nil
	}
	for first := true; first || !p.Done(); first = false {
		if !c.canSend() {
			cs.sub.MarkDirty()
			return nil
		}
		nr, nd, withIDs := c.buildPatch(p)
		if c.inflight+c.buf.Len() > protocol.MaxInFlightBytes && len(c.unacked) > 0 {
			cs.sub.MarkDirty()
			return nil
		}
		sentDict, err := c.flushDict()
		if err != nil {
			return err
		}
		if sentDict { // flushDict reused c.buf: re-encode the patch frame from c.rows
			c.rebuildPatch(p, nr, nd, withIDs)
		}
		seq, n, err := c.writeFrame(protocol.KindPatch, cs.qid)
		if err != nil {
			return err
		}
		c.unacked[seq] = n
		c.inflight += n
		p.Commit(nr, nd, withIDs)
	}
	cs.last = time.Now()
	cs.urgent = false
	return nil
}

// buildPatch encodes the next frame of p into c.buf (rows go through c.rows first so the dictionary
// is complete before the frame is written) and returns how much of p it covers.
func (c *conn) buildPatch(p *view.Pending) (nr, nd int, withIDs bool) {
	withIDs = p.SendIDs
	removed := p.RemainingRemoved()
	nd = min(len(removed), maxRemovedPerF)
	budget := protocol.MaxFrameBytes - HeaderLen - 64 - nd*5
	if withIDs {
		budget -= 4*len(p.IDs) + 8
	}
	c.rows.Reset()
	for i, r := range p.RemainingRows() {
		mark := c.rows.Len()
		_ = view.EncodeRow(c.rowsEnc, c.d, &r)
		if c.rows.Len() > budget && i > 0 {
			c.rows.Truncate(mark)
			break
		}
		nr++
	}
	c.rebuildPatch(p, nr, nd, withIDs)
	return
}

// rebuildPatch writes the patch frame for the given slice of p into c.buf using the encoded rows in c.rows.
func (c *conn) rebuildPatch(p *view.Pending, nr, nd int, withIDs bool) {
	c.begin()
	keys := 3
	if withIDs {
		keys++
	}
	if nr > 0 {
		keys++
	}
	if nd > 0 {
		keys++
	}
	e := c.enc
	_ = e.EncodeMapLen(keys)
	_ = e.EncodeString("gen")
	_ = e.EncodeUint32(p.Gen)
	_ = e.EncodeString("total")
	_ = e.EncodeInt(int64(p.Total))
	_ = e.EncodeString("offset")
	_ = e.EncodeInt(int64(p.Offset))
	if withIDs {
		b := make([]byte, 4*len(p.IDs))
		for i, id := range p.IDs {
			binary.LittleEndian.PutUint32(b[4*i:], id)
		}
		_ = e.EncodeString("ids")
		_ = e.EncodeBytes(b)
	}
	if nr > 0 {
		_ = e.EncodeString("rows")
		_ = e.EncodeArrayLen(nr)
		c.buf.Write(c.rows.Bytes())
	}
	if nd > 0 {
		_ = e.EncodeString("removed")
		_ = e.EncodeArrayLen(nd)
		for _, id := range p.RemainingRemoved()[:nd] {
			_ = e.EncodeUint32(id)
		}
	}
}

// ---- inbound --------------------------------------------------------------------------

type rawRequest struct {
	Method string             `msgpack:"method"`
	Params msgpack.RawMessage `msgpack:"params"`
}

func (c *conn) handle(m inMsg) error {
	switch m.kind {
	case protocol.KindAck:
		if n, ok := c.unacked[m.seq]; ok {
			delete(c.unacked, m.seq)
			c.inflight -= n
		}
		return nil
	case protocol.KindRequest:
		var req rawRequest
		if err := msgpack.Unmarshal(m.payload, &req); err != nil {
			return c.respondErr(m.channel, "bad_request", "malformed request")
		}
		return c.dispatch(m.channel, &req)
	}
	return nil // unknown kinds are ignored
}

func (c *conn) respond(id uint32, result any) error {
	return c.send(protocol.KindResponse, id, protocol.Response{Result: result})
}

func (c *conn) respondErr(id uint32, code, msg string) error {
	return c.send(protocol.KindResponse, id, protocol.Response{Error: &protocol.ErrorBody{Code: code, Message: msg}})
}

func decodeParams(req *rawRequest, v any) error {
	if len(req.Params) == 0 {
		return errors.New("missing params")
	}
	return msgpack.Unmarshal(req.Params, v)
}

func (c *conn) dispatch(id uint32, req *rawRequest) error {
	switch req.Method {
	case "clusters.list":
		return c.respond(id, c.s.opt.Manager.List())
	case "clusters.select":
		var p protocol.ClustersSelectParams
		if err := decodeParams(req, &p); err != nil {
			return c.respondErr(id, "bad_request", err.Error())
		}
		st, err := c.s.opt.Manager.Select(p.IDs)
		if err != nil {
			return c.respondErr(id, "not_found", err.Error())
		}
		c.statesDue = true
		if err := c.respond(id, st); err != nil {
			return err
		}
		return c.sendStates()
	case "kinds.list":
		return c.respond(id, protocol.KindsListResult{Kinds: Kinds})
	case "view.subscribe":
		var p protocol.ViewSubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return c.respondErr(id, "bad_request", err.Error())
		}
		if _, dup := c.subs[p.QID]; dup || p.QID == 0 {
			return c.respondErr(id, "bad_request", "qid must be non-zero and unique")
		}
		sub, err := c.s.opt.Engine.Subscribe(p.Query, p.Gen, p.Window)
		if err != nil {
			return c.respondErr(id, errCode(err), err.Error())
		}
		c.subs[p.QID] = &connSub{qid: p.QID, sub: sub, urgent: true}
		return c.respond(id, protocol.ViewSubscribeResult{Columns: protocol.PodColumns})
	case "view.update":
		var p protocol.ViewUpdateParams
		if err := decodeParams(req, &p); err != nil {
			return c.respondErr(id, "bad_request", err.Error())
		}
		cs := c.subs[p.QID]
		if cs == nil {
			return c.respondErr(id, "not_found", "no such subscription")
		}
		if err := cs.sub.Update(p.Query, p.Gen, p.Window); err != nil {
			return c.respondErr(id, errCode(err), err.Error())
		}
		cs.urgent = true
		return c.respond(id, nil)
	case "view.unsubscribe":
		var p protocol.ViewUnsubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return c.respondErr(id, "bad_request", err.Error())
		}
		if cs := c.subs[p.QID]; cs != nil {
			cs.sub.Close()
			delete(c.subs, p.QID)
		}
		return c.respond(id, nil)
	}
	return c.respondErr(id, "bad_request", "unknown method "+req.Method)
}

func errCode(err error) string {
	var bq *view.BadQueryError
	if errors.As(err, &bq) {
		return "bad_request"
	}
	return "internal"
}
