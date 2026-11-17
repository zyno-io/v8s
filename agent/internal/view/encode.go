package view

import (
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/ingest"
)

func intern(s string) ingest.Str { return ingest.Intern(s) }

// Dict maps interned strings to per-socket dictionary ids (0 = empty string).
type Dict interface {
	ID(s ingest.Str) uint32
}

// EncodeRow writes [id, version, cluster, name, namespace, ready, containers, status, statusTone,
// restarts, node, createdAt] matching protocol.PodColumns.
func EncodeRow(enc *msgpack.Encoder, d Dict, r *PendRow) error {
	row := r.Row
	_ = enc.EncodeArrayLen(12)
	_ = enc.EncodeUint32(r.ID)
	_ = enc.EncodeUint32(row.Ver())
	_ = enc.EncodeUint32(d.ID(r.Cluster))
	_ = enc.EncodeString(row.Name())
	_ = enc.EncodeUint32(d.ID(row.NS()))
	_ = enc.EncodeUint(uint64(row.Ready()))
	_ = enc.EncodeUint(uint64(row.Total()))
	_ = enc.EncodeUint32(d.ID(row.Status()))
	_ = enc.EncodeUint8(row.Tone())
	_ = enc.EncodeInt32(row.Restarts())
	_ = enc.EncodeUint32(d.ID(row.Node()))
	return enc.EncodeInt(row.CreatedMs())
}
