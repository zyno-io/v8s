package ingest

import (
	"context"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// PodAPI is the subset of the typed pods client the ingest path uses. The real clientset
// (CoreV1().Pods("")) and the fake source both implement it, so both feed the same projection path.
type PodAPI interface {
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// Hooks lets the owner observe connection progress. All fields are optional.
type Hooks struct {
	// OnSyncProgress reports rows received so far during an initial list / streaming list.
	OnSyncProgress func(n int)
	// OnError reports a failed list or watch call (or a watch error event).
	OnError func(err error)
	// OnOK reports that a list or watch call succeeded.
	OnOK func()
}

// ListerWatcher implements cache.ListerWatcher (and the WithContext variants) over a PodAPI,
// projecting every decoded pod into a *PodRow before the reflector sees it.
type ListerWatcher struct {
	API   PodAPI
	Hooks Hooks
	count atomic.Int64
}

func (l *ListerWatcher) ListWithContext(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	if opts.Continue == "" {
		l.count.Store(0)
	}
	pl, err := l.API.List(ctx, opts)
	if err != nil {
		l.err(err)
		return nil, err
	}
	out := &PodRowList{Items: make([]*PodRow, len(pl.Items))}
	out.ResourceVersion = pl.ResourceVersion
	out.Continue = pl.Continue
	out.RemainingItemCount = pl.RemainingItemCount
	for i := range pl.Items {
		out.Items[i] = Project(&pl.Items[i])
	}
	if l.Hooks.OnOK != nil {
		l.Hooks.OnOK()
	}
	if l.Hooks.OnSyncProgress != nil {
		l.Hooks.OnSyncProgress(int(l.count.Add(int64(len(out.Items)))))
	}
	return out, nil
}

func (l *ListerWatcher) WatchWithContext(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	src, err := l.API.Watch(ctx, opts)
	if err != nil {
		l.err(err)
		return nil, err
	}
	if l.Hooks.OnOK != nil {
		l.Hooks.OnOK()
	}
	if opts.SendInitialEvents != nil && *opts.SendInitialEvents {
		l.count.Store(0)
	}
	w := &projWatch{src: src, out: make(chan watch.Event), done: make(chan struct{}), l: l,
		initial: opts.SendInitialEvents != nil && *opts.SendInitialEvents}
	go w.run()
	return w, nil
}

func (l *ListerWatcher) List(opts metav1.ListOptions) (runtime.Object, error) {
	return l.ListWithContext(context.Background(), opts)
}

func (l *ListerWatcher) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return l.WatchWithContext(context.Background(), opts)
}

func (l *ListerWatcher) err(err error) {
	if l.Hooks.OnError != nil {
		l.Hooks.OnError(err)
	}
}

type projWatch struct {
	src     watch.Interface
	out     chan watch.Event
	done    chan struct{}
	once    sync.Once
	l       *ListerWatcher
	initial bool
}

func (w *projWatch) ResultChan() <-chan watch.Event { return w.out }

func (w *projWatch) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.src.Stop()
	})
}

func (w *projWatch) run() {
	defer close(w.out)
	inInitial := w.initial
	n := 0
	for ev := range w.src.ResultChan() {
		switch o := ev.Object.(type) {
		case *corev1.Pod:
			if ev.Type == watch.Bookmark {
				row := ProjectBookmark(o)
				if row.flags&flagInitialEventsEnd != 0 && inInitial {
					inInitial = false
					if h := w.l.Hooks.OnSyncProgress; h != nil {
						h(n)
					}
				}
				ev.Object = row
			} else {
				ev.Object = Project(o)
				if inInitial && ev.Type == watch.Added {
					n++
					if h := w.l.Hooks.OnSyncProgress; h != nil && n%256 == 0 {
						h(n)
					}
				}
			}
		case *metav1.Status:
			w.l.err(&watchStatusError{o})
		}
		select {
		case w.out <- ev:
		case <-w.done:
			return
		}
	}
}

type watchStatusError struct{ s *metav1.Status }

func (e *watchStatusError) Error() string { return "watch error: " + e.s.Message }
