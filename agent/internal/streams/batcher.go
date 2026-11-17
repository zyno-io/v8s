package streams

import (
	"bytes"
	"context"
	"time"
)

// Batcher groups lines into frames of at most MaxBytes, flushed at the latest MaxDelay after the first line of
// a batch (docs/performance.md §8: <= 100 ms, <= 64 KiB). A single line larger than MaxBytes is split.
type Batcher struct {
	MaxBytes int
	MaxDelay time.Duration
	// Flush sends one frame; it may block (credit).
	Flush func(ctx context.Context, frame []byte) error
}

// Run consumes lines (each already terminated by '\n') until in is closed or ctx ends; it flushes what is left.
func (b *Batcher) Run(ctx context.Context, in <-chan []byte) error {
	var buf bytes.Buffer
	var timerC <-chan time.Time
	var timer *time.Timer
	stop := func() {
		if timer != nil {
			timer.Stop()
			timer, timerC = nil, nil
		}
	}
	defer stop()
	flush := func() error {
		stop()
		if buf.Len() == 0 {
			return nil
		}
		out := append([]byte(nil), buf.Bytes()...)
		buf.Reset()
		return b.Flush(ctx, out)
	}
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case line, ok := <-in:
			if !ok {
				return flush()
			}
			for len(line) > 0 {
				room := b.MaxBytes - buf.Len()
				if room <= 0 || (buf.Len() > 0 && len(line) > room) {
					if err := flush(); err != nil {
						return err
					}
					continue
				}
				n := min(len(line), room)
				buf.Write(line[:n])
				line = line[n:]
			}
			if buf.Len() >= b.MaxBytes {
				if err := flush(); err != nil {
					return err
				}
			} else if buf.Len() > 0 && timer == nil {
				timer = time.NewTimer(b.MaxDelay)
				timerC = timer.C
			}
		case <-timerC:
			timer, timerC = nil, nil
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
