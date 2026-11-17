package ingest

import (
	"context"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// API is a list/watch source for one (cluster, GVR): typed objects for curated kinds (decoded from
// protobuf by the real implementation), *TableResult for Table-mode kinds. The real clientset
// and the fake source both implement it, so both feed the same projection path.
type API interface {
	List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// PodAPI is the typed pods client subset (the fake pod generator implements it directly).
type PodAPI interface {
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// PodAdapter lifts a PodAPI to an API.
type PodAdapter struct{ PodAPI }

func (a PodAdapter) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	pl, err := a.PodAPI.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return pl, nil
}

// Hooks lets the owner observe connection progress. All fields are optional.
type Hooks struct {
	// OnSyncProgress reports rows received so far during an initial list / streaming list.
	OnSyncProgress func(n int)
	// OnError reports a failed list or watch call (or a watch error event).
	OnError func(err error)
	// OnOK reports that a list or watch call succeeded.
	OnOK func()
	// OnRows receives each batch of projected rows (a list page, or up to 256 initial watch events) while
	// the first sync is in progress, so the owner can show them provisionally before the reflector's Replace.
	OnRows func(rows []*Row)
}

// ListerWatcher implements cache.ListerWatcher (and the WithContext variants) over an API,
// projecting every decoded object into a *Row before the reflector sees it.
type ListerWatcher struct {
	API API
	// Mod is the kind's module (nil: pods). For a Table-mode kind whose schema is not known yet, set
	// Generic instead; Mod is filled in from the first columnDefinitions.
	Mod     *Module
	Generic *GenericSpec
	Hooks   Hooks
	count   atomic.Int64

	mu      sync.Mutex
	mapping []int // table column → cell index of the latest columnDefinitions
}

// GenericSpec identifies a Table-mode kind (from discovery) so its module can be created lazily.
type GenericSpec struct {
	GVR        string
	Kind       string
	Namespaced bool
	Short      []string
}

func (l *ListerWatcher) mod() *Module {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Mod == nil && l.Generic == nil {
		l.Mod = Lookup(PodsGVR)
	}
	return l.Mod
}

// IsWatchListSemanticsUnSupported makes the reflector use a paginated LIST + watch for Table kinds
// (a streaming list cannot deliver columnDefinitions for an empty kind).
func (l *ListerWatcher) IsWatchListSemanticsUnSupported() bool {
	return l.Generic != nil || (l.Mod != nil && l.Mod.Table)
}

// tableModule resolves the module and mapping for a table result carrying columns (idempotent).
func (l *ListerWatcher) tableModule(t *TableResult) (*Module, []int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Mod == nil && l.Generic != nil && len(t.Cols) > 0 {
		g := l.Generic
		l.Mod = GenericModule(g.GVR, g.Kind, g.Namespaced, g.Short, t.Cols)
	}
	if l.Mod != nil && (len(t.Cols) > 0 || l.mapping == nil) {
		l.mapping = l.Mod.mapFor(t.Cols)
	}
	return l.Mod, l.mapping
}

func (l *ListerWatcher) tweak(opts metav1.ListOptions) metav1.ListOptions {
	if m := l.mod(); m != nil && m.Tweak != nil {
		m.Tweak(&opts)
	} else if m == nil && l.Generic != nil {
		if cm := Lookup(l.Generic.GVR); cm != nil && cm.Tweak != nil {
			cm.Tweak(&opts)
		}
	}
	return opts
}

func (l *ListerWatcher) ListWithContext(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	if opts.Continue == "" {
		l.count.Store(0)
		if opts.ResourceVersion == "0" {
			// resourceVersion=0 is served from the watch cache without pagination; the list must page.
			opts.ResourceVersion = ""
		}
	}
	obj, err := l.API.List(ctx, l.tweak(opts))
	if err != nil {
		l.err(err)
		return nil, err
	}
	out := &RowList{}
	if t, ok := obj.(*TableResult); ok {
		out.ResourceVersion, out.Continue, out.RemainingItemCount = t.RV, t.Continue, t.Remaining
		m, mapping := l.tableModule(t)
		if m == nil {
			l.err(errNoSchema)
			return nil, errNoSchema
		}
		out.Items = make([]*Row, len(t.Rows))
		for i := range t.Rows {
			out.Items[i] = m.ProjectTableRow(&t.Rows[i], mapping)
		}
	} else {
		la, err := listMeta(obj)
		if err != nil {
			l.err(err)
			return nil, err
		}
		out.ResourceVersion, out.Continue, out.RemainingItemCount = la.GetResourceVersion(), la.GetContinue(), la.GetRemainingItemCount()
		m := l.mod()
		if m == nil || m.Each == nil {
			l.err(errNoSchema)
			return nil, errNoSchema
		}
		m.Each(obj, func(r *Row) { out.Items = append(out.Items, r) })
	}
	if l.Hooks.OnOK != nil {
		l.Hooks.OnOK()
	}
	if l.Hooks.OnRows != nil {
		l.Hooks.OnRows(out.Items)
	}
	if l.Hooks.OnSyncProgress != nil {
		l.Hooks.OnSyncProgress(int(l.count.Add(int64(len(out.Items)))))
	}
	return out, nil
}

func (l *ListerWatcher) WatchWithContext(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	src, err := l.API.Watch(ctx, l.tweak(opts))
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
	var batch []*Row
	flush := func() {
		if len(batch) > 0 && w.l.Hooks.OnRows != nil {
			w.l.Hooks.OnRows(batch)
		}
		batch = nil
	}
	defer flush()
	emit := func(ev watch.Event, row *Row) bool {
		if row != nil {
			if ev.Type == watch.Bookmark {
				if row.flags&flagInitialEventsEnd != 0 && inInitial {
					flush()
					inInitial = false
					if h := w.l.Hooks.OnSyncProgress; h != nil {
						h(n)
					}
				}
			} else if inInitial && ev.Type == watch.Added {
				batch = append(batch, row)
				if len(batch) >= 256 {
					flush()
				}
				n++
				if h := w.l.Hooks.OnSyncProgress; h != nil && n%256 == 0 {
					h(n)
				}
			}
			ev.Object = row
		}
		select {
		case w.out <- ev:
			return true
		case <-w.done:
			return false
		}
	}
	for ev := range w.src.ResultChan() {
		switch o := ev.Object.(type) {
		case *metav1.Status:
			w.l.err(apierrors.FromObject(o))
			if !emit(ev, nil) {
				return
			}
		case *TableResult:
			if o.Bookmark {
				if !emit(ev, bookmarkRow(o.RV, o.Annotations)) {
					return
				}
				continue
			}
			m, mapping := w.l.tableModule(o)
			if m == nil {
				continue
			}
			for i := range o.Rows {
				if !emit(ev, m.ProjectTableRow(&o.Rows[i], mapping)) {
					return
				}
			}
		default:
			if ev.Type == watch.Bookmark {
				rv, ann := objAccessor(ev.Object)
				if !emit(ev, bookmarkRow(rv, ann)) {
					return
				}
				continue
			}
			m := w.l.mod()
			if m == nil || m.ProjectObj == nil {
				continue
			}
			row := m.ProjectObj(ev.Object)
			if row == nil {
				continue
			}
			if !emit(ev, row) {
				return
			}
		}
	}
}
