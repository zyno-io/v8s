package wire

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ops"
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
	qid      uint32
	sub      *view.Sub
	scope    *cluster.Scope // keeps the kind's watches alive for this subscription
	gvr      string
	clusters []string
	last     time.Time
	urgent   bool // client-initiated change: bypass the tick once
}

// objSub is a live object.subscribe.
type objSub struct{ cancel context.CancelFunc }

type conn struct {
	s   *Server
	ws  *websocket.Conn
	in  chan inMsg
	seq uint32

	subs     map[uint32]*connSub
	pendingQ map[uint32]bool // view.subscribe requests still resolving their kind
	objs     map[uint32]*objSub
	post     chan func() error // work to run on the connection goroutine (async responses, object frames)
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
	c := &conn{s: s, ws: ws, in: make(chan inMsg, 16), subs: map[uint32]*connSub{}, unacked: map[uint32]int{},
		pendingQ: map[uint32]bool{}, objs: map[uint32]*objSub{}, post: make(chan func() error, 64)}
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
			if cs.scope != nil {
				cs.scope.Close()
			}
		}
		for _, o := range c.objs {
			o.cancel()
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
		case f := <-c.post:
			if f() != nil {
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
		_ = view.EncodeRow(c.rowsEnc, c.d, p.Mod, &r)
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

// async runs fn off the connection goroutine and delivers its response on it.
func (c *conn) async(id uint32, fn func(ctx context.Context) (any, error)) {
	go func() {
		res, err := fn(c.ctx)
		c.postf(func() error {
			if err != nil {
				return c.respondBody(id, errBody(err))
			}
			return c.respond(id, res)
		})
	}()
}

// postf queues work for the connection goroutine.
func (c *conn) postf(f func() error) {
	select {
	case c.post <- f:
	case <-c.ctx.Done():
	}
}

func (c *conn) respondBody(id uint32, b protocol.ErrorBody) error {
	return c.send(protocol.KindResponse, id, protocol.Response{Error: &b})
}

// errBody converts an error from the managers into a protocol error.
func errBody(err error) protocol.ErrorBody {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe.Body
	}
	var bq *view.BadQueryError
	switch {
	case errors.As(err, &bq):
		return protocol.ErrorBody{Code: "bad_request", Message: err.Error()}
	case errors.Is(err, cluster.ErrNotServed):
		return protocol.ErrorBody{Code: "not_found", Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return protocol.ErrorBody{Code: "unavailable", Message: "timed out waiting for the kind's schema"}
	case errors.Is(err, cluster.ErrBudget):
		return protocol.ErrorBody{Code: "unavailable", Message: err.Error()}
	}
	return protocol.ErrorBody{Code: "internal", Message: err.Error()}
}

func queryGVR(q protocol.ViewQuery) string {
	if q.GVR == "" {
		return ingest.PodsGVR
	}
	return q.GVR
}

// resolve finds the module and opens the watch scope for a query. Known kinds resolve inline; a generic
// kind waits (off the connection goroutine) for its first columnDefinitions. cb runs on the connection goroutine.
func (c *conn) resolve(q protocol.ViewQuery, cb func(mod *ingest.Module, scope *cluster.Scope, err error)) {
	gvr, mgr := queryGVR(q), c.s.opt.Manager
	if mod := ingest.Lookup(gvr); mod != nil {
		cb(mod, mgr.OpenScope(gvr, q.Clusters), nil)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
		defer cancel()
		mod, err := mgr.ResolveModule(ctx, gvr, q.Clusters)
		var scope *cluster.Scope
		if err == nil {
			scope = mgr.OpenScope(gvr, q.Clusters)
		}
		c.postf(func() error { cb(mod, scope, err); return nil })
	}()
}

func (c *conn) dispatch(id uint32, req *rawRequest) error {
	opt := c.s.opt
	bad := func(err error) error { return c.respondErr(id, "bad_request", err.Error()) }
	switch req.Method {
	case "clusters.list":
		return c.respond(id, opt.Manager.List())
	case "clusters.select":
		var p protocol.ClustersSelectParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		st, err := opt.Manager.Select(p.IDs)
		if err != nil {
			return c.respondErr(id, "not_found", err.Error())
		}
		c.statesDue = true
		if err := c.respond(id, st); err != nil {
			return err
		}
		return c.sendStates()
	case "kinds.list":
		return c.respond(id, protocol.KindsListResult{Kinds: opt.Manager.Kinds()})
	case "view.subscribe":
		var p protocol.ViewSubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		if _, dup := c.subs[p.QID]; dup || c.pendingQ[p.QID] || p.QID == 0 {
			return c.respondErr(id, "bad_request", "qid must be non-zero and unique")
		}
		c.pendingQ[p.QID] = true
		c.resolve(p.Query, func(mod *ingest.Module, scope *cluster.Scope, err error) {
			delete(c.pendingQ, p.QID)
			if err != nil {
				_ = c.respondBody(id, errBody(err))
				return
			}
			sub, err := opt.Engine.Subscribe(p.Query, p.Gen, p.Window)
			if err != nil {
				scope.Close()
				_ = c.respondBody(id, errBody(err))
				return
			}
			c.subs[p.QID] = &connSub{qid: p.QID, sub: sub, scope: scope, gvr: queryGVR(p.Query), clusters: p.Query.Clusters, urgent: true}
			_ = c.respond(id, protocol.ViewSubscribeResult{Columns: mod.Columns()})
		})
		return nil
	case "view.update":
		var p protocol.ViewUpdateParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		cs := c.subs[p.QID]
		if cs == nil {
			return c.respondErr(id, "not_found", "no such subscription")
		}
		if p.Query != nil && (queryGVR(*p.Query) != cs.gvr || !slices.Equal(normClusters(p.Query.Clusters), normClusters(cs.clusters))) {
			gvrChanged := queryGVR(*p.Query) != cs.gvr
			c.resolve(*p.Query, func(mod *ingest.Module, scope *cluster.Scope, err error) {
				if err != nil {
					_ = c.respondBody(id, errBody(err))
					return
				}
				if c.subs[p.QID] != cs {
					scope.Close()
					_ = c.respondErr(id, "not_found", "no such subscription")
					return
				}
				if err := cs.sub.Update(p.Query, p.Gen, p.Window); err != nil {
					scope.Close()
					_ = c.respondBody(id, errBody(err))
					return
				}
				cs.scope.Close()
				cs.scope, cs.gvr, cs.clusters, cs.urgent = scope, queryGVR(*p.Query), p.Query.Clusters, true
				if gvrChanged { // the client needs the new schema
					_ = c.respond(id, protocol.ViewSubscribeResult{Columns: mod.Columns()})
				} else {
					_ = c.respond(id, nil)
				}
			})
			return nil
		}
		if err := cs.sub.Update(p.Query, p.Gen, p.Window); err != nil {
			return c.respondBody(id, errBody(err))
		}
		cs.urgent = true
		return c.respond(id, nil)
	case "view.unsubscribe":
		var p protocol.ViewUnsubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		if cs := c.subs[p.QID]; cs != nil {
			cs.sub.Close()
			if cs.scope != nil {
				cs.scope.Close()
			}
			delete(c.subs, p.QID)
		}
		return c.respond(id, nil)

	case "object.get":
		var p protocol.ObjectGetParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) { return opt.Ops.Get(ctx, p) })
		return nil
	case "object.subscribe":
		var p protocol.ObjectSubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		if _, dup := c.objs[p.OID]; dup || p.OID == 0 {
			return c.respondErr(id, "bad_request", "oid must be non-zero and unique")
		}
		ctx, cancel := context.WithCancel(c.ctx)
		c.objs[p.OID] = &objSub{cancel: cancel}
		oid := p.OID
		go func() {
			_ = opt.Ops.Watch(ctx, p.Ref, p.KeepManagedFields, func(f protocol.ObjectFrame) {
				c.postf(func() error {
					if c.objs[oid] == nil {
						return nil
					}
					return c.send(protocol.KindObject, oid, f)
				})
			})
		}()
		return c.respond(id, nil)
	case "object.unsubscribe":
		var p protocol.ObjectUnsubscribeParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		if o := c.objs[p.OID]; o != nil {
			o.cancel()
			delete(c.objs, p.OID)
		}
		return c.respond(id, nil)
	case "object.dryRun":
		var p protocol.ObjectDryRunParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) { return opt.Ops.DryRun(ctx, p) })
		return nil
	case "object.apply":
		var p protocol.ObjectApplyParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) { return opt.Ops.Apply(ctx, p) })
		return nil
	case "object.delete":
		var p protocol.ObjectDeleteParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) { return opt.Ops.Delete(ctx, p) })
		return nil
	case "object.action":
		var p protocol.ObjectActionParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) { return opt.Ops.Action(ctx, p) })
		return nil
	case "access.check":
		var p protocol.AccessCheckParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) {
			return protocol.AccessCheckResult{Results: opt.Ops.Access(ctx, p.Checks)}, nil
		})
		return nil
	case "settings.get":
		return c.respond(id, opt.Settings.Get())
	case "settings.set":
		var p protocol.SettingsSetParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		st, err := opt.Settings.Patch(p.Patch)
		if err != nil {
			return c.respondErr(id, "bad_request", err.Error())
		}
		opt.Manager.Changed() // linked contexts and groups may have changed the cluster list
		return c.respond(id, st)
	case "search":
		var p protocol.SearchParams
		if err := decodeParams(req, &p); err != nil {
			return bad(err)
		}
		c.async(id, func(ctx context.Context) (any, error) {
			return protocol.SearchResult{Results: opt.Manager.Search(p.Text, p.Limit)}, nil
		})
		return nil
	}
	return c.respondErr(id, "bad_request", "unknown method "+req.Method)
}

func normClusters(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}
