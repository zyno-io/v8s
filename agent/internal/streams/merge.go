package streams

import (
	"context"
	"time"
)

// Line is one log line of a pod container with its parsed timestamp.
type Line struct {
	TS   time.Time
	Src  string // "<pod>/<container>"
	Text []byte // the line without the timestamp prefix, terminated by '\n'
}

// Merge interleaves per-source line channels by timestamp. A source's next line is waited for (up to wait)
// before a line of another source is emitted, so a slow source does not get out of order; once wait expired
// the smallest available head goes out. Within a source order is preserved. It returns when every source is
// closed, emit fails, or ctx ends.
func Merge(ctx context.Context, srcs []<-chan Line, wait time.Duration, emit func(Line) error) error {
	n := len(srcs)
	type ev struct {
		i  int
		l  Line
		ok bool
	}
	evs := make(chan ev)
	req := make([]chan struct{}, n)
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range srcs {
		req[i] = make(chan struct{}, 1)
		go func(i int) {
			for {
				select {
				case <-req[i]:
				case <-mctx.Done():
					return
				}
				select {
				case l, ok := <-srcs[i]:
					select {
					case evs <- ev{i, l, ok}:
					case <-mctx.Done():
						return
					}
					if !ok {
						return
					}
				case <-mctx.Done():
					return
				}
			}
		}(i)
		req[i] <- struct{}{}
	}
	heads := make([]*Line, n)
	done := make([]bool, n)
	open := n
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	expired := false
	for open > 0 || anyHead(heads) {
		missing, have := false, false
		for i := 0; i < n; i++ {
			if heads[i] != nil {
				have = true
			} else if !done[i] {
				missing = true
			}
		}
		if have && (!missing || expired) {
			best := -1
			for i, h := range heads {
				if h != nil && (best < 0 || h.TS.Before(heads[best].TS)) {
					best = i
				}
			}
			l := *heads[best]
			heads[best] = nil
			expired = false
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			if err := emit(l); err != nil {
				return err
			}
			if !done[best] {
				req[best] <- struct{}{}
			}
			continue
		}
		var tc <-chan time.Time
		if have && timer == nil {
			timer = time.NewTimer(wait)
		}
		if timer != nil {
			tc = timer.C
		}
		select {
		case e := <-evs:
			if !e.ok {
				done[e.i] = true
				open--
			} else {
				l := e.l
				heads[e.i] = &l
			}
		case <-tc:
			timer = nil
			expired = true
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return nil
}

func anyHead(h []*Line) bool {
	for _, x := range h {
		if x != nil {
			return true
		}
	}
	return false
}
