package ingest

import (
	"context"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// restFor picks the typed clientset REST client (protobuf-capable) that serves group/version.
func restFor(cs *kubernetes.Clientset, group, version string) (rest.Interface, error) {
	switch group + "/" + version {
	case "/v1":
		return cs.CoreV1().RESTClient(), nil
	case "apps/v1":
		return cs.AppsV1().RESTClient(), nil
	case "batch/v1":
		return cs.BatchV1().RESTClient(), nil
	case "networking.k8s.io/v1":
		return cs.NetworkingV1().RESTClient(), nil
	case "autoscaling/v2":
		return cs.AutoscalingV2().RESTClient(), nil
	case "policy/v1":
		return cs.PolicyV1().RESTClient(), nil
	case "discovery.k8s.io/v1":
		return cs.DiscoveryV1().RESTClient(), nil
	}
	return nil, fmt.Errorf("no typed client for %s/%s", group, version)
}

// TypedAPI lists and watches a curated kind through its typed REST client; the API server answers in
// protobuf and the client decodes straight into typed objects (mode A).
type TypedAPI struct {
	C        rest.Interface
	Resource string
	NewList  func() runtime.Object
	// Namespace, when set, narrows list and watch to that namespace.
	Namespace string
}

// Namespacer narrows a list/watch source to one namespace (the per-object events watch).
type Namespacer interface{ InNamespace(ns string) API }

// InNamespace returns a copy of the API scoped to one namespace.
func (a *TypedAPI) InNamespace(ns string) API { c := *a; c.Namespace = ns; return &c }

func (a *TypedAPI) req() *rest.Request {
	r := a.C.Get()
	if a.Namespace != "" {
		r = r.Namespace(a.Namespace)
	}
	return r.Resource(a.Resource)
}

func (a *TypedAPI) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	out := a.NewList()
	err := a.req().VersionedParams(&opts, scheme.ParameterCodec).Do(ctx).Into(out)
	return out, err
}

func (a *TypedAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return a.req().VersionedParams(&opts, scheme.ParameterCodec).Watch(ctx)
}

// TableAPI lists and watches any resource as a server-side Table with PartialObjectMetadata (mode C).
type TableAPI struct {
	C                        rest.Interface
	Group, Version, Resource string
}

func (a *TableAPI) path() string {
	if a.Group == "" {
		return "/api/" + a.Version + "/" + a.Resource
	}
	return "/apis/" + a.Group + "/" + a.Version + "/" + a.Resource
}

func (a *TableAPI) req(opts metav1.ListOptions) *rest.Request {
	return a.C.Get().AbsPath(a.path()).SetHeader("Accept", TableAccept).
		VersionedParams(&opts, scheme.ParameterCodec).Param("includeObject", string(metav1.IncludeMetadata))
}

func (a *TableAPI) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	b, err := a.req(opts).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return DecodeTable(b)
}

func (a *TableAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	body, err := a.req(opts).Stream(ctx)
	if err != nil {
		return nil, err
	}
	return NewTableWatch(body), nil
}

// NewTableWatch turns a JSON watch stream of Table events into a watch.Interface whose objects are
// *TableResult (or *metav1.Status for errors).
func NewTableWatch(body io.ReadCloser) watch.Interface {
	w := &tableWatch{body: body, ch: make(chan watch.Event), done: make(chan struct{})}
	go w.run()
	return w
}

type tableWatch struct {
	body io.ReadCloser
	ch   chan watch.Event
	done chan struct{}
}

func (w *tableWatch) ResultChan() <-chan watch.Event { return w.ch }

func (w *tableWatch) Stop() {
	select {
	case <-w.done:
	default:
		close(w.done)
		_ = w.body.Close()
	}
}

func (w *tableWatch) run() {
	defer close(w.ch)
	defer w.body.Close()
	dec := NewTableEventDecoder(w.body)
	for {
		ev, err := dec.Next()
		if err != nil {
			return // EOF or a stopped watch: the reflector re-establishes it
		}
		var out watch.Event
		switch {
		case ev.Status != nil:
			out = watch.Event{Type: watch.Error, Object: ev.Status}
		default:
			out = watch.Event{Type: watch.EventType(ev.Type), Object: ev.Table}
		}
		select {
		case w.ch <- out:
		case <-w.done:
			return
		}
	}
}

// APIFor builds the list/watch API for a module on a real clientset.
func APIFor(cs *kubernetes.Clientset, m *Module, group, version, resource string) (API, error) {
	if m != nil && m.NewAPI != nil {
		return m.NewAPI(cs.CoreV1().RESTClient(), group, version, resource), nil
	}
	if m != nil && m.Curated && !m.Table {
		c, err := restFor(cs, group, version)
		if err != nil {
			return nil, err
		}
		return &TypedAPI{C: c, Resource: resource, NewList: m.NewList}, nil
	}
	return &TableAPI{C: cs.CoreV1().RESTClient(), Group: group, Version: version, Resource: resource}, nil
}
