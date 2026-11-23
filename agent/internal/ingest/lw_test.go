package ingest

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

func init() {
	// The reflector only tries WatchList when this gate is on (the agent sets it in cmd/v8s).
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
}

// scriptAPI hands out a fresh fake watcher per Watch call and records the options it saw.
type scriptAPI struct {
	mu      sync.Mutex
	opts    []metav1.ListOptions
	lists   int
	watches []*watch.RaceFreeFakeWatcher
}

func (a *scriptAPI) List(context.Context, metav1.ListOptions) (runtime.Object, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lists++
	return &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "5"}, Items: []corev1.Pod{*mkPod(corev1.PodRunning, 1)}}, nil
}

func (a *scriptAPI) Watch(_ context.Context, o metav1.ListOptions) (watch.Interface, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts = append(a.opts, o)
	w := watch.NewRaceFreeFake()
	a.watches = append(a.watches, w)
	return w, nil
}

func (a *scriptAPI) counts() (lists, watchLists, plain int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range a.opts {
		if o.SendInitialEvents != nil && *o.SendInitialEvents {
			watchLists++
		} else {
			plain++
		}
	}
	return a.lists, watchLists, plain
}

func (a *scriptAPI) last() *watch.RaceFreeFakeWatcher {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.watches[len(a.watches)-1]
}

func initialOpts() metav1.ListOptions {
	t := true
	return metav1.ListOptions{SendInitialEvents: &t, ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan, AllowWatchBookmarks: true}
}

func endBookmark() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "9", Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}}}
}

// drain reads w until it closes, returning the events seen.
func drain(t *testing.T, w watch.Interface) []watch.Event {
	t.Helper()
	var evs []watch.Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-w.ResultChan():
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("watch did not close")
		}
	}
}

func shrinkPacing(t *testing.T, base, max, healthy time.Duration) {
	ob, om, oh := watchBackoffBase, watchBackoffMax, minHealthyWatch
	watchBackoffBase, watchBackoffMax, minHealthyWatch = base, max, healthy
	t.Cleanup(func() { watchBackoffBase, watchBackoffMax, minHealthyWatch = ob, om, oh })
}

func TestWatchListTruncatedFallsBack(t *testing.T) {
	api := &scriptAPI{}
	var errs, trunc, rows atomic.Int32
	lw := &ListerWatcher{API: api, Hooks: Hooks{
		OnError:              func(error) { errs.Add(1) },
		OnWatchListTruncated: func() { trunc.Add(1) },
		OnRows:               func(r []*Row) { rows.Add(int32(len(r))) },
	}}
	w, err := lw.WatchWithContext(context.Background(), initialOpts())
	if err != nil {
		t.Fatal(err)
	}
	src := api.last()
	src.Add(mkPod(corev1.PodRunning, 1))
	src.Stop()
	evs := drain(t, w)
	if len(evs) != 2 || evs[0].Type != watch.Added || evs[1].Type != watch.Error {
		t.Fatalf("events: %+v", evs)
	}
	if st, ok := evs[1].Object.(*metav1.Status); !ok || st.Reason != metav1.StatusReasonUnknown {
		t.Fatalf("error object: %#v", evs[1].Object)
	}
	if errs.Load() != 0 || trunc.Load() != 1 || rows.Load() != 1 {
		t.Fatalf("errs=%d trunc=%d rows=%d", errs.Load(), trunc.Load(), rows.Load())
	}
	_, wl, _ := api.counts()
	if _, err := lw.WatchWithContext(context.Background(), initialOpts()); err == nil {
		t.Fatal("second watch-list should fail")
	}
	if _, wl2, _ := api.counts(); wl2 != wl {
		t.Fatal("API called for a known-broken watch-list")
	}
	if errs.Load() != 0 {
		t.Fatal("OnError called")
	}
}

func TestWatchListWithBookmarkNotBroken(t *testing.T) {
	api := &scriptAPI{}
	var trunc atomic.Int32
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnWatchListTruncated: func() { trunc.Add(1) }}}
	w, err := lw.WatchWithContext(context.Background(), initialOpts())
	if err != nil {
		t.Fatal(err)
	}
	src := api.last()
	src.Add(mkPod(corev1.PodRunning, 1))
	src.Action(watch.Bookmark, endBookmark())
	src.Stop()
	for _, ev := range drain(t, w) {
		if ev.Type == watch.Error {
			t.Fatalf("unexpected error event: %+v", ev)
		}
	}
	if lw.watchListBroken.Load() || trunc.Load() != 0 {
		t.Fatal("marked broken")
	}
}

func TestCallerStopNotBroken(t *testing.T) {
	api := &scriptAPI{}
	lw := &ListerWatcher{API: api}
	w, err := lw.WatchWithContext(context.Background(), initialOpts())
	if err != nil {
		t.Fatal(err)
	}
	api.last().Add(mkPod(corev1.PodRunning, 1))
	<-w.ResultChan()
	w.Stop()
	drain(t, w)
	if lw.watchListBroken.Load() || lw.shortStreak.Load() != 0 {
		t.Fatalf("broken=%v streak=%d", lw.watchListBroken.Load(), lw.shortStreak.Load())
	}
}

func closeQuick(t *testing.T, lw *ListerWatcher, api *scriptAPI) time.Duration {
	t.Helper()
	start := time.Now()
	w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	api.last().Stop()
	drain(t, w)
	return waited
}

func TestPacingAfterQuickCloses(t *testing.T) {
	shrinkPacing(t, 100*time.Millisecond, 400*time.Millisecond, time.Hour)
	api := &scriptAPI{}
	lw := &ListerWatcher{API: api}
	if d := closeQuick(t, lw, api); d > 50*time.Millisecond {
		t.Fatalf("first watch waited %v", d)
	}
	if d := closeQuick(t, lw, api); d < 70*time.Millisecond { // base 100ms, jitter -20%
		t.Fatalf("second watch did not wait: %v", d)
	}
	if lw.shortStreak.Load() != 2 {
		t.Fatalf("streak %d", lw.shortStreak.Load())
	}
	if d := closeQuick(t, lw, api); d < 150*time.Millisecond { // 200ms -20%
		t.Fatalf("third watch waited only %v", d)
	}
}

func TestPacingCtxCancel(t *testing.T) {
	shrinkPacing(t, 10*time.Second, time.Minute, time.Hour)
	api := &scriptAPI{}
	var errs atomic.Int32
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnError: func(error) { errs.Add(1) }}}
	closeQuick(t, lw, api)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := lw.WatchWithContext(ctx, metav1.ListOptions{})
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	if errs.Load() != 0 {
		t.Fatal("OnError called on cancel")
	}
	if _, _, plain := api.counts(); plain != 1 {
		t.Fatalf("API called during cancelled wait: %d", plain)
	}
}

func TestPacingResetsAfterHealthyWatch(t *testing.T) {
	shrinkPacing(t, 10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond)
	api := &scriptAPI{}
	lw := &ListerWatcher{API: api}
	closeQuick(t, lw, api)
	if lw.shortStreak.Load() != 1 {
		t.Fatalf("streak %d", lw.shortStreak.Load())
	}
	w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	api.last().Stop()
	drain(t, w)
	if lw.shortStreak.Load() != 0 {
		t.Fatalf("streak not reset: %d", lw.shortStreak.Load())
	}
}

func TestReflectorFallsBackToListOnTruncatedWatchList(t *testing.T) {
	api := &scriptAPI{}
	var trunc atomic.Int32
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnWatchListTruncated: func() { trunc.Add(1) }}}
	st := cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc)
	refl := cache.NewReflectorWithOptions(lw, &Row{}, st, cache.ReflectorOptions{Name: "t", TypeDescription: "pods"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = refl.ListAndWatchWithContext(ctx) }()

	// Truncate the watch-list stream the moment it opens.
	deadline := time.After(5 * time.Second)
	for {
		api.mu.Lock()
		var first *watch.RaceFreeFakeWatcher
		if len(api.watches) > 0 {
			first = api.watches[0]
		}
		api.mu.Unlock()
		if first != nil {
			first.Add(mkPod(corev1.PodRunning, 1))
			first.Stop()
			break
		}
		select {
		case <-deadline:
			t.Fatal("reflector never issued a watch-list (feature gate off?)")
		case <-time.After(5 * time.Millisecond):
		}
	}
	for {
		if _, _, plain := api.counts(); plain >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no plain watch after fallback")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond) // a spin would show as extra requests
	lists, wl, plain := api.counts()
	if wl != 1 || lists != 1 || plain != 1 || trunc.Load() != 1 {
		t.Fatalf("watchLists=%d lists=%d plain=%d trunc=%d", wl, lists, plain, trunc.Load())
	}
	cancel()
	<-done
}

// An error event followed by the source closing (the API server's normal way to end a watch with an error)
// is the reflector's to handle: it must not mark the watch-list broken or count as an early close.
func TestErrorThenCloseNotBroken(t *testing.T) {
	api := &scriptAPI{}
	var trunc atomic.Int32
	lw := &ListerWatcher{API: api, Hooks: Hooks{OnError: func(error) {}, OnWatchListTruncated: func() { trunc.Add(1) }}}
	w, err := lw.WatchWithContext(context.Background(), initialOpts())
	if err != nil {
		t.Fatal(err)
	}
	src := api.last()
	src.Add(mkPod(corev1.PodRunning, 1))
	src.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
	src.Stop()
	evs := drain(t, w)
	if len(evs) != 2 || evs[1].Type != watch.Error {
		t.Fatalf("events: %+v", evs)
	}
	if lw.watchListBroken.Load() || trunc.Load() != 0 || lw.shortStreak.Load() != 0 {
		t.Fatalf("broken=%v trunc=%d streak=%d", lw.watchListBroken.Load(), trunc.Load(), lw.shortStreak.Load())
	}
}

// A cancelled context closing the source is not a truncation, whatever order Stop arrives in.
func TestCtxCancelNotBroken(t *testing.T) {
	api := &scriptAPI{}
	lw := &ListerWatcher{API: api}
	ctx, cancel := context.WithCancel(context.Background())
	w, err := lw.WatchWithContext(ctx, initialOpts())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	api.last().Stop() // the transport noticing the cancel, before the caller's Stop
	drain(t, w)
	if lw.watchListBroken.Load() || lw.shortStreak.Load() != 0 {
		t.Fatalf("broken=%v streak=%d", lw.watchListBroken.Load(), lw.shortStreak.Load())
	}
}

// A stream a proxy cuts early while events flow is not paced: re-watching at once keeps the data live.
func TestShortWatchWithEventsNotPaced(t *testing.T) {
	shrinkPacing(t, time.Second, time.Second, time.Hour)
	api := &scriptAPI{}
	lw := &ListerWatcher{API: api}
	for range 3 {
		w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{ResourceVersion: "5"})
		if err != nil {
			t.Fatal(err)
		}
		src := api.last()
		src.Modify(mkPod(corev1.PodRunning, 1))
		src.Stop()
		drain(t, w)
	}
	if n := lw.shortStreak.Load(); n != 0 {
		t.Fatalf("streak %d after event-delivering watches", n)
	}
}

// A successful LIST clears the streak, so the watch after a relist (or after a park/unpark) is not delayed.
func TestListResetsStreak(t *testing.T) {
	lw := &ListerWatcher{API: &scriptAPI{}}
	lw.shortStreak.Store(5)
	if _, err := lw.ListWithContext(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := lw.shortStreak.Load(); n != 0 {
		t.Fatalf("streak %d after LIST", n)
	}
}

// tableOnce answers every List with a one-column Table (or an error) and records the limit it was asked for.
type tableOnce struct {
	err   error
	limit atomic.Int64
	lists atomic.Int32
}

func (a *tableOnce) List(_ context.Context, o metav1.ListOptions) (runtime.Object, error) {
	a.lists.Add(1)
	a.limit.Store(o.Limit)
	if a.err != nil {
		return nil, a.err
	}
	return &TableResult{Cols: []TableCol{{Name: "Name", Type: "string"}, {Name: "Data", Type: "integer"}}, RV: "1"}, nil
}

func (a *tableOnce) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return watch.NewRaceFreeFake(), nil
}

func TestProbeSchemaLearnsModuleFromOneRow(t *testing.T) {
	const gvr = "probe.example.io/v1/widgets"
	defer Unregister(gvr)()
	api := &tableOnce{}
	l := &ListerWatcher{API: api, Generic: &GenericSpec{GVR: gvr, Kind: "Widget", Namespaced: true}}
	l.ProbeSchema(context.Background())
	if l.Mod == nil || l.Mod.GVR != gvr || api.limit.Load() != 1 {
		t.Fatalf("module %v after a limit-%d probe", l.Mod, api.limit.Load())
	}
	l.ProbeSchema(context.Background()) // known now: no second request
	if n := api.lists.Load(); n != 1 {
		t.Fatalf("%d probe lists, want 1", n)
	}
}

func TestProbeSchemaIgnoresErrorsAndKnownModules(t *testing.T) {
	const gvr = "probe.example.io/v1/gadgets"
	defer Unregister(gvr)()
	api := &tableOnce{err: errors.New("boom")}
	l := &ListerWatcher{API: api, Generic: &GenericSpec{GVR: gvr, Kind: "Gadget"}}
	l.ProbeSchema(context.Background())
	if l.Mod != nil {
		t.Fatal("module set after a failed probe")
	}
	// A curated kind (Mod set, no Generic) is never probed.
	api2 := &tableOnce{}
	(&ListerWatcher{API: api2, Mod: Lookup(PodsGVR)}).ProbeSchema(context.Background())
	if api2.lists.Load() != 0 {
		t.Fatal("probed a kind that already has a module")
	}
}
