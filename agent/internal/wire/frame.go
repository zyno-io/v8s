// Package wire serves the agent protocol (docs/protocol.md) over a WebSocket.
package wire

import (
	"encoding/binary"
	"errors"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// HeaderLen is the size of the frame header: [u8 kind][u32 channel BE][u32 seq BE].
const HeaderLen = 9

var errShortFrame = errors.New("short frame")

// PutHeader writes the frame header into b[:HeaderLen].
func PutHeader(b []byte, kind protocol.FrameKind, channel, seq uint32) {
	b[0] = byte(kind)
	binary.BigEndian.PutUint32(b[1:], channel)
	binary.BigEndian.PutUint32(b[5:], seq)
}

// ParseFrame splits a frame into header fields and payload.
func ParseFrame(b []byte) (kind protocol.FrameKind, channel, seq uint32, payload []byte, err error) {
	if len(b) < HeaderLen {
		return 0, 0, 0, nil, errShortFrame
	}
	return protocol.FrameKind(b[0]), binary.BigEndian.Uint32(b[1:]), binary.BigEndian.Uint32(b[5:]), b[HeaderLen:], nil
}

// dict is the per-socket string dictionary; ids start at 1, 0 means the empty string.
type dict struct {
	ids     map[ingest.Str]uint32
	next    uint32
	pending []string
}

func newDict() *dict { return &dict{ids: make(map[ingest.Str]uint32)} }

func (d *dict) ID(s ingest.Str) uint32 {
	if id, ok := d.ids[s]; ok {
		return id
	}
	v := s.Value()
	if v == "" {
		return 0
	}
	d.next++
	d.ids[s] = d.next
	d.pending = append(d.pending, v)
	return d.next
}

// take returns the next DictAdd whose encoded size stays under maxBytes (approximate), or false.
func (d *dict) take(maxBytes int) (protocol.DictAdd, bool) {
	if len(d.pending) == 0 {
		return protocol.DictAdd{}, false
	}
	base := d.next - uint32(len(d.pending)) + 1
	size, n := 0, 0
	for _, s := range d.pending {
		size += len(s) + 5
		if size > maxBytes && n > 0 {
			break
		}
		n++
	}
	add := protocol.DictAdd{Base: base, Strings: d.pending[:n:n]}
	d.pending = d.pending[n:]
	return add, true
}
