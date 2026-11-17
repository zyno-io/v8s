package helm

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// MetaSource lists and watches release Secrets as metadata only. Watch events carry
// *metav1.PartialObjectMetadata (errors a *metav1.Status); no implementation ever returns Secret data.
type MetaSource interface {
	List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

const (
	listAccept  = "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1"
	watchAccept = "application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1"
	pageSize    = 500
)

// ---- real: a REST client with the metadata Accept header ------------------------------------

type restMetaSource struct {
	c  rest.Interface
	ns string
}

// NewRESTMetaSource reads Secrets of all namespaces as PartialObjectMetadata through c (a core/v1 REST
// client), so the API server never sends the release payloads.
func NewRESTMetaSource(c rest.Interface) MetaSource { return &restMetaSource{c: c} }

// NewNamespacedRESTMetaSource is NewRESTMetaSource limited to one namespace.
func NewNamespacedRESTMetaSource(c rest.Interface, ns string) MetaSource {
	return &restMetaSource{c: c, ns: ns}
}

func (s *restMetaSource) path() string {
	if s.ns == "" {
		return "/api/v1/secrets"
	}
	return "/api/v1/namespaces/" + url.PathEscape(s.ns) + "/secrets"
}

func (s *restMetaSource) req(accept string, opts metav1.ListOptions) *rest.Request {
	return s.c.Get().AbsPath(s.path()).SetHeader("Accept", accept).VersionedParams(&opts, scheme.ParameterCodec)
}

// DecodeMetaObject decodes one PartialObjectMetadata from JSON.
func DecodeMetaObject(raw []byte) (runtime.Object, error) {
	var m metav1.PartialObjectMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *restMetaSource) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	b, err := s.req(listAccept, opts).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := ingest.DecodePartialList(b, DecodeMetaObject)
	if err != nil {
		return nil, err
	}
	out := &metav1.PartialObjectMetadataList{ListMeta: pl.ListMeta}
	out.Items = make([]metav1.PartialObjectMetadata, 0, len(pl.Items))
	for _, it := range pl.Items {
		out.Items = append(out.Items, *it.(*metav1.PartialObjectMetadata))
	}
	return out, nil
}

func (s *restMetaSource) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	body, err := s.req(watchAccept, opts).Stream(ctx)
	if err != nil {
		return nil, err
	}
	return ingest.NewPartialWatch(body, DecodeMetaObject), nil
}

// ---- typed clientset wrapper (fake mode) ------------------------------------------------------

type typedMetaSource struct {
	cs kubernetes.Interface
	ns string
}

// NewTypedMetaSource wraps a typed clientset: it lists and watches Secrets with the label selector and
// strips the data. The client-go fake ignores label selectors on Watch, so the wrapper filters itself.
func NewTypedMetaSource(cs kubernetes.Interface) MetaSource { return &typedMetaSource{cs: cs} }

// NewNamespacedTypedMetaSource is NewTypedMetaSource limited to one namespace.
func NewNamespacedTypedMetaSource(cs kubernetes.Interface, ns string) MetaSource {
	return &typedMetaSource{cs: cs, ns: ns}
}

func secretMeta(s *corev1.Secret) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: *s.ObjectMeta.DeepCopy(),
	}
}

func (s *typedMetaSource) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	l, err := s.cs.CoreV1().Secrets(s.ns).List(ctx, metav1.ListOptions{LabelSelector: opts.LabelSelector})
	if err != nil {
		return nil, err
	}
	out := &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{ResourceVersion: l.ResourceVersion}}
	for i := range l.Items {
		out.Items = append(out.Items, *secretMeta(&l.Items[i]))
	}
	return out, nil
}

func (s *typedMetaSource) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	sel, err := labels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}
	src, err := s.cs.CoreV1().Secrets(s.ns).Watch(ctx, metav1.ListOptions{LabelSelector: opts.LabelSelector, ResourceVersion: opts.ResourceVersion, AllowWatchBookmarks: opts.AllowWatchBookmarks})
	if err != nil {
		return nil, err
	}
	w := &typedWatch{src: src, out: make(chan watch.Event), done: make(chan struct{})}
	go func() {
		defer close(w.out)
		for ev := range src.ResultChan() {
			if sec, ok := ev.Object.(*corev1.Secret); ok {
				if ev.Type != watch.Bookmark && !sel.Matches(labels.Set(sec.Labels)) {
					continue
				}
				ev.Object = secretMeta(sec)
			}
			select {
			case w.out <- ev:
			case <-w.done:
				return
			}
		}
	}()
	return w, nil
}

type typedWatch struct {
	src  watch.Interface
	out  chan watch.Event
	done chan struct{}
	stop bool
}

func (w *typedWatch) ResultChan() <-chan watch.Event { return w.out }
func (w *typedWatch) Stop() {
	select {
	case <-w.done:
	default:
		close(w.done)
		w.src.Stop()
	}
}

// listAll pages through every item of a MetaSource list. It returns the items and the list's
// resourceVersion (the snapshot every page shares).
func listAll(ctx context.Context, src MetaSource, selector string) ([]metav1.PartialObjectMetadata, string, error) {
	var items []metav1.PartialObjectMetadata
	rv := ""
	opts := metav1.ListOptions{LabelSelector: selector, Limit: pageSize}
	for first := true; ; first = false {
		pl, err := src.List(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		if first {
			rv = pl.ResourceVersion
		}
		items = append(items, pl.Items...)
		if pl.Continue == "" {
			return items, rv, nil
		}
		opts.Continue = pl.Continue
	}
}
