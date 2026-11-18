package view

import (
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func intern(s string) ingest.Str { return ingest.Intern(s) }

// Dict maps interned strings to per-socket dictionary ids (0 = empty string).
type Dict interface {
	ID(s ingest.Str) uint32
}

// EncodeRow writes [id, version, cell0, cell1, …] in the module's column order (protocol.Column.Type
// says how each cell is encoded).
func EncodeRow(enc *msgpack.Encoder, d Dict, mod *ingest.Module, r *PendRow) error {
	row := r.Row
	_ = enc.EncodeArrayLen(2 + len(mod.Cols))
	_ = enc.EncodeUint32(r.ID)
	_ = enc.EncodeUint32(row.Ver())
	for i := range mod.Cols {
		c := &mod.Cols[i]
		switch c.Src {
		case ingest.SrcCluster:
			_ = enc.EncodeUint32(d.ID(r.Cluster))
		case ingest.SrcName:
			_ = enc.EncodeString(row.Name())
		case ingest.SrcNS:
			_ = enc.EncodeUint32(d.ID(row.NS()))
		case ingest.SrcOwner:
			_ = enc.EncodeUint32(d.ID(row.OwnerName()))
		case ingest.SrcTone:
			_ = enc.EncodeUint8(row.Tone())
		case ingest.SrcCreated:
			_ = enc.EncodeInt(row.CreatedMs())
		case ingest.SrcIP:
			_ = enc.EncodeString(row.IPText(c.IPField))
		case ingest.SrcCell:
			cell := row.Cell(c.Idx)
			switch c.Type {
			case protocol.ColDict:
				_ = enc.EncodeUint32(d.ID(cell.S))
			case protocol.ColStr:
				_ = enc.EncodeString(cell.S.Value())
			default: // int, time
				_ = enc.EncodeInt(cell.N)
			}
		}
	}
	return nil
}
