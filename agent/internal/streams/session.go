package streams

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// runLogs serves a logs stream: lines are batched (<= 100 ms / 64 KiB) and sent as binary frames while the
// client has credit; {"credit": n} text frames add credit.
func (m *Manager) runLogs(ctx context.Context, t Transport, open LogOpener, spec *LogSpec, lim *limits) {
	defer lim.Stop()
	credit := NewCredit(protocol.InitialLogCredit)
	go func() { // client -> agent: credit
		for {
			_, data, err := t.Read(ctx)
			if err != nil {
				return
			}
			lim.Touch()
			var msg protocol.StreamClientMsg
			if json.Unmarshal(data, &msg) == nil {
				credit.Add(msg.Credit)
			}
		}
	}()
	lines := make(chan []byte, 32)
	var perr error
	pdone := make(chan struct{})
	go func() { perr = streamLogs(ctx, open, spec, lines); close(pdone) }()
	b := &Batcher{MaxBytes: protocol.LogBatchBytes, MaxDelay: protocol.LogBatchMillis * time.Millisecond,
		Flush: func(ctx context.Context, frame []byte) error {
			if err := credit.Take(ctx, int64(len(frame))); err != nil {
				return err
			}
			lim.Touch()
			return t.Write(ctx, true, frame)
		}}
	berr := b.Run(ctx, lines)
	cancelled := berr != nil
	var err error
	if cancelled {
		err = berr
	} else {
		<-pdone
		err = perr
	}
	if c := context.Cause(ctx); c == ErrIdle || c == ErrCap {
		err = c
	}
	sendEnd(ctx, t, err)
}

// kubeLogOpener adapts a Runtime.
func kubeLogOpener(rt kube.Runtime) LogOpener { return rt.PodLogs }
