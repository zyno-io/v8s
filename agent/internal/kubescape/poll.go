package kubescape

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

// PollInterval is how often the fallback re-lists a kind whose server does not support watch.
const PollInterval = 5 * time.Minute

// PollingAPI wraps the list/watch source of an aggregated-API kind. Watch is used when the server supports it;
// when it does not (the extension API server answers 404/405/406/501 to a watch request) the kind falls back to a
// paged list every Interval, compared by uid and resourceVersion, and the differences are delivered as ordinary
// watch events, so the reflector and every consumer above it see no difference.
type PollingAPI struct {
	Inner    ingest.API
	Interval time.Duration
	// After returns a channel that fires after d (default time.After); tests inject their own clock.
	After func(d time.Duration) <-chan time.Time

	mu   sync.Mutex
	last map[string]string // uid → resourceVersion of the latest list
}

// NewPollingAPI wraps inner.
func NewPollingAPI(inner ingest.API, interval time.Duration) *PollingAPI {
	return &PollingAPI{Inner: inner, Interval: interval, After: time.After}
}

func (a *PollingAPI) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	obj, err := a.Inner.List(ctx, opts)
	if err == nil {
		// the reflector pages through the kind (limit 500): the known state is every page of the latest list
		snap := snapshot(obj)
		a.mu.Lock()
		if opts.Continue == "" || a.last == nil {
			a.last = snap
		} else {
			for k, v := range snap {
				a.last[k] = v
			}
		}
		a.mu.Unlock()
	}
	return obj, err
}

func snapshot(obj runtime.Object) map[string]string {
	out := map[string]string{}
	meta.EachListItem(obj, func(o runtime.Object) error { //nolint:errcheck
		if m, err := meta.Accessor(o); err == nil {
			out[string(m.GetUID())] = m.GetResourceVersion()
		}
		return nil
	})
	return out
}

// Unsupported reports whether err means the server cannot serve a watch of this kind.
func Unsupported(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsMethodNotSupported(err) || apierrors.IsNotAcceptable(err) {
		return true
	}
	if st, ok := err.(apierrors.APIStatus); ok {
		switch st.Status().Code {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusNotImplemented:
			return true
		}
	}
	return false
}

func (a *PollingAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	w, err := a.Inner.Watch(ctx, opts)
	if err == nil || !Unsupported(err) {
		return w, err
	}
	return a.poll(ctx), nil
}

type pollWatch struct {
	ch   chan watch.Event
	done chan struct{}
	once sync.Once
}

func (w *pollWatch) ResultChan() <-chan watch.Event { return w.ch }
func (w *pollWatch) Stop()                          { w.once.Do(func() { close(w.done) }) }

func (a *PollingAPI) poll(ctx context.Context) watch.Interface {
	w := &pollWatch{ch: make(chan watch.Event), done: make(chan struct{})}
	go func() {
		defer close(w.ch)
		a.mu.Lock()
		var known map[string]string
		if a.last != nil { // an empty kind is a known (empty) state
			known = make(map[string]string, len(a.last))
			for k, v := range a.last {
				known[k] = v
			}
		}
		a.mu.Unlock()
		if known == nil {
			if obj, err := a.listAll(ctx); err == nil {
				known = snapshot(obj)
			} else {
				known = map[string]string{}
			}
		}
		for {
			select {
			case <-w.done:
				return
			case <-ctx.Done():
				return
			case <-a.After(a.Interval):
			}
			obj, err := a.listAll(ctx)
			if err != nil {
				continue // keep the previous state; try again next interval
			}
			now := map[string]string{}
			var events []watch.Event
			meta.EachListItem(obj, func(o runtime.Object) error { //nolint:errcheck
				m, err := meta.Accessor(o)
				if err != nil {
					return nil
				}
				uid, rv := string(m.GetUID()), m.GetResourceVersion()
				now[uid] = rv
				switch old, ok := known[uid]; {
				case !ok:
					events = append(events, watch.Event{Type: watch.Added, Object: o})
				case old != rv:
					events = append(events, watch.Event{Type: watch.Modified, Object: o})
				}
				return nil
			})
			for uid := range known {
				if _, ok := now[uid]; !ok {
					events = append(events, watch.Event{Type: watch.Deleted, Object: tombstone(uid)})
				}
			}
			known = now
			a.mu.Lock()
			a.last = now
			a.mu.Unlock()
			for _, ev := range events {
				select {
				case w.ch <- ev:
				case <-w.done:
					return
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return w
}

// tombstone is a Summary carrying only the uid (the store deletes by uid).
func tombstone(uid string) runtime.Object {
	s := &Summary{}
	s.UID = types.UID(uid)
	return s
}

// listAll pages through the kind (limit 500) and returns the concatenated list.
func (a *PollingAPI) listAll(ctx context.Context) (runtime.Object, error) {
	var all *ingest.PartialList
	opts := metav1.ListOptions{Limit: 500}
	for {
		obj, err := a.Inner.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		pl, ok := obj.(*ingest.PartialList)
		if !ok {
			return obj, nil
		}
		if all == nil {
			all = &ingest.PartialList{ListMeta: pl.ListMeta}
		}
		all.Items = append(all.Items, pl.Items...)
		if pl.Continue == "" {
			all.Continue = ""
			return all, nil
		}
		opts.Continue = pl.Continue
	}
}
