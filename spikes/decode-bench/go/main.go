// Go decode benchmark using the real k8s.io/api types (what client-go informers hold).
// Measures: JSON decode of a PodList, protobuf decode of the same list, and retained heap for
// full objects vs managedFields-stripped objects vs PartialObjectMetadata.
//
// Retained heap is the absolute live heap after every input buffer has been dropped: apimachinery's
// protobuf decoding can alias strings/bytes into the input buffer, so "heap delta while the buffer is
// still live" undercounts badly.
//
// usage: go run . ../out/pods-10k.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
	"unique"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func live() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func mb(b uint64) string { return fmt.Sprintf("%.1f MB", float64(b)/1048576) }

func timeit(label string, runs int, fn func()) {
	ts := make([]time.Duration, runs)
	for i := range ts {
		t := time.Now()
		fn()
		ts[i] = time.Since(t)
	}
	sort.Slice(ts, func(a, b int) bool { return ts[a] < ts[b] })
	fmt.Printf("%-56s median %.1f ms\n", label, float64(ts[runs/2].Microseconds())/1000)
}

func strip(l *corev1.PodList) {
	for i := range l.Items {
		l.Items[i].ManagedFields = nil
		delete(l.Items[i].Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	}
}

// protoOf returns the protobuf encoding of the list in path, optionally stripped.
func protoOf(path string, stripped bool) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var l corev1.PodList
	if err := json.Unmarshal(raw, &l); err != nil {
		panic(err)
	}
	if stripped {
		strip(&l)
	}
	b, err := l.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

// podRow is the kind of projection the agent would hold for a pod list + problem detectors:
// identity, labels, owner, placement, and container state summaries. Repeated strings are interned.
type containerState struct {
	name     unique.Handle[string]
	image    unique.Handle[string]
	ready    bool
	restarts int32
	reason   unique.Handle[string] // waiting/terminated reason, e.g. CrashLoopBackOff
	lastTerm unique.Handle[string] // lastState.terminated.reason, e.g. OOMKilled
}

type podRow struct {
	uid        string
	name       string
	namespace  unique.Handle[string]
	labels     map[unique.Handle[string]]unique.Handle[string]
	ownerKind  unique.Handle[string]
	ownerName  unique.Handle[string]
	node       unique.Handle[string]
	phase      unique.Handle[string]
	podIP      string
	created    int64
	deleting   bool
	conditions uint8 // bitset: PodScheduled, Initialized, ContainersReady, Ready
	containers []containerState
}

func project(p *corev1.Pod) podRow {
	in := unique.Make[string]
	r := podRow{uid: strings.Clone(string(p.UID)), name: strings.Clone(p.Name), namespace: in(p.Namespace), node: in(p.Spec.NodeName),
		phase: in(string(p.Status.Phase)), podIP: strings.Clone(p.Status.PodIP), created: p.CreationTimestamp.Unix(), deleting: p.DeletionTimestamp != nil}
	r.labels = make(map[unique.Handle[string]]unique.Handle[string], len(p.Labels))
	for k, v := range p.Labels {
		r.labels[in(k)] = in(v)
	}
	if len(p.OwnerReferences) > 0 {
		r.ownerKind, r.ownerName = in(p.OwnerReferences[0].Kind), in(p.OwnerReferences[0].Name)
	}
	for i, c := range p.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			r.conditions |= 1 << uint(i%8)
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		cs := containerState{name: in(c.Name), image: in(c.Image), ready: c.Ready, restarts: c.RestartCount}
		if c.State.Waiting != nil {
			cs.reason = in(c.State.Waiting.Reason)
		}
		if c.LastTerminationState.Terminated != nil {
			cs.lastTerm = in(c.LastTerminationState.Terminated.Reason)
		}
		r.containers = append(r.containers, cs)
	}
	return r
}

func main() {
	path := "../out/pods-10k.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	func() {
		raw, _ := os.ReadFile(path)
		fmt.Printf("input: %s JSON\n", mb(uint64(len(raw))))
		timeit("encoding/json -> corev1.PodList", 3, func() {
			var l corev1.PodList
			if err := json.Unmarshal(raw, &l); err != nil {
				panic(err)
			}
		})
		pb := protoOf(path, false)
		fmt.Printf("%-56s %s\n", "protobuf size of same list", mb(uint64(len(pb))))
		timeit("protobuf -> corev1.PodList", 5, func() {
			var l corev1.PodList
			if err := l.Unmarshal(pb); err != nil {
				panic(err)
			}
		})
	}()

	fmt.Printf("%-56s %s\n", "protobuf size, stripped list", mb(uint64(len(protoOf(path, true)))))

	measure := func(label string, stripped, metaOnly bool) {
		base := live()
		var held any
		func() {
			pb := protoOf(path, stripped)
			var l corev1.PodList
			if err := l.Unmarshal(pb); err != nil {
				panic(err)
			}
			if metaOnly {
				metas := make([]metav1.PartialObjectMetadata, len(l.Items))
				for i := range l.Items {
					metas[i] = metav1.PartialObjectMetadata{ObjectMeta: *l.Items[i].ObjectMeta.DeepCopy()}
				}
				held = metas
			} else {
				held = l.Items
			}
		}()
		fmt.Printf("%-56s %s\n", label, mb(live()-base))
		runtime.KeepAlive(held)
	}
	measure("retained heap, full typed objects", false, false)
	measure("retained heap, stripped typed objects (informer transform)", true, false)
	measure("retained heap, PartialObjectMetadata (stripped)", true, true)

	// Projected rows, decoded one object at a time (as the projecting ListerWatcher does per event).
	base := live()
	var rows []podRow
	func() {
		pb := protoOf(path, false)
		var l corev1.PodList
		if err := l.Unmarshal(pb); err != nil {
			panic(err)
		}
		rows = make([]podRow, len(l.Items))
		for i := range l.Items {
			rows[i] = project(&l.Items[i])
		}
	}()
	fmt.Printf("%-56s %s\n", "retained heap, projected pod rows (interned)", mb(live()-base))
	runtime.KeepAlive(rows)
}
