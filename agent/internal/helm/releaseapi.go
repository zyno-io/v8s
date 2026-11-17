package helm

import (
	"context"
	"sort"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

const helmSelector = "owner=helm"

// releaseAPI turns the Secret metadata stream into a release stream: one object per release at its
// latest revision. The revision table is shared by List (which rebuilds it) and Watch (which updates it),
// because a watch starts from a resourceVersion with no seed.
type releaseAPI struct {
	src MetaSource
	mu  sync.Mutex
	rel map[string]map[int]revision // "ns/name" -> version -> revision
}

// NewReleaseAPI builds the ingest.API of the release list over a metadata source.
func NewReleaseAPI(src MetaSource) ingest.API {
	return &releaseAPI{src: src, rel: map[string]map[int]revision{}}
}

func relKey(ns, name string) string { return ns + "/" + name }

func latest(revs map[int]revision) (revision, bool) {
	var best revision
	found := false
	for v, r := range revs {
		if !found || v > best.version {
			best, found = r, true
		}
	}
	return best, found
}

func (a *releaseAPI) List(ctx context.Context, _ metav1.ListOptions) (runtime.Object, error) {
	items, rv, err := listAll(ctx, a.src, helmSelector)
	if err != nil {
		return nil, err
	}
	table := map[string]map[int]revision{}
	for i := range items {
		r, ok := revisionOf(&items[i])
		if !ok {
			continue
		}
		k := relKey(r.ns, r.name)
		if table[k] == nil {
			table[k] = map[int]revision{}
		}
		table[k][r.version] = r
	}
	a.mu.Lock()
	a.rel = table
	a.mu.Unlock()
	out := &ReleaseList{ListMeta: metav1.ListMeta{ResourceVersion: rv}}
	for _, revs := range table {
		l, _ := latest(revs)
		out.Items = append(out.Items, l.release(l.rv))
	}
	sort.Slice(out.Items, func(i, j int) bool {
		x, y := out.Items[i], out.Items[j]
		if x.Namespace != y.Namespace {
			return x.Namespace < y.Namespace
		}
		return x.Name < y.Name
	})
	return out, nil
}

func (a *releaseAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	src, err := a.src.Watch(ctx, metav1.ListOptions{LabelSelector: helmSelector, ResourceVersion: opts.ResourceVersion,
		AllowWatchBookmarks: opts.AllowWatchBookmarks, TimeoutSeconds: opts.TimeoutSeconds})
	if err != nil {
		return nil, err
	}
	w := &relWatch{src: src, out: make(chan watch.Event), done: make(chan struct{})}
	go func() {
		defer close(w.out)
		for ev := range src.ResultChan() {
			for _, o := range a.translate(ev) {
				select {
				case w.out <- o:
				case <-w.done:
					return
				}
			}
		}
	}()
	return w, nil
}

// translate folds one Secret event into the revision table and returns the release event it causes (none
// when the latest revision's visible facts did not change).
func (a *releaseAPI) translate(ev watch.Event) []watch.Event {
	m, ok := ev.Object.(*metav1.PartialObjectMetadata)
	if !ok || ev.Type == watch.Error || ev.Type == watch.Bookmark {
		return []watch.Event{ev} // errors and bookmarks pass through unchanged
	}
	r, ok := revisionOf(m)
	if !ok {
		return nil
	}
	k := relKey(r.ns, r.name)
	a.mu.Lock()
	defer a.mu.Unlock()
	before, hadBefore := latest(a.rel[k])
	switch ev.Type {
	case watch.Added, watch.Modified:
		if a.rel[k] == nil {
			a.rel[k] = map[int]revision{}
		}
		a.rel[k][r.version] = r
	case watch.Deleted:
		delete(a.rel[k], r.version)
		if len(a.rel[k]) == 0 {
			delete(a.rel, k)
		}
	default:
		return nil
	}
	after, hasAfter := latest(a.rel[k])
	same := func() bool {
		x, y := before, after
		x.rv, y.rv = "", ""
		return x == y
	}
	switch {
	case !hadBefore && hasAfter:
		return []watch.Event{{Type: watch.Added, Object: after.release(m.ResourceVersion)}}
	case hadBefore && !hasAfter:
		return []watch.Event{{Type: watch.Deleted, Object: before.release(m.ResourceVersion)}}
	case hadBefore && hasAfter && !same():
		return []watch.Event{{Type: watch.Modified, Object: after.release(m.ResourceVersion)}}
	}
	return nil
}

type relWatch struct {
	src  watch.Interface
	out  chan watch.Event
	done chan struct{}
	once sync.Once
}

func (w *relWatch) ResultChan() <-chan watch.Event { return w.out }
func (w *relWatch) Stop() {
	w.once.Do(func() { close(w.done); w.src.Stop() })
}
