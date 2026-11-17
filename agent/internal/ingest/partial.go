package ingest

// Ingest mode B: typed partial JSON. The kind is read as JSON and each object is decoded into a struct that
// declares only the fields the projection needs (encoding/json skips the rest cheaply); the projection
// then drops the object. Used for curated CRDs (Flux).

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// PartialList is a decoded list of mode B objects.
type PartialList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []runtime.Object
}

func (l *PartialList) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (l *PartialList) DeepCopyObject() runtime.Object {
	c := *l
	c.Items = append([]runtime.Object(nil), l.Items...)
	return &c
}

// PartialEach is the Module.Each of a mode B kind: every item through m.ProjectObj.
func PartialEach(m *Module) func(runtime.Object, func(*Row)) {
	return func(l runtime.Object, fn func(*Row)) {
		pl, ok := l.(*PartialList)
		if !ok {
			return
		}
		for _, it := range pl.Items {
			if r := m.ProjectObj(it); r != nil {
				fn(r)
			}
		}
	}
}

// PartialAPI lists and watches a CRD kind as JSON, decoding each item with decode.
type PartialAPI struct {
	C                        rest.Interface
	Group, Version, Resource string
	Decode                   func(raw []byte) (runtime.Object, error)
}

// NewPartialAPI builds the Module.NewAPI of a mode B kind.
func NewPartialAPI(decode func(raw []byte) (runtime.Object, error)) func(c rest.Interface, g, v, r string) API {
	return func(c rest.Interface, g, v, r string) API {
		return &PartialAPI{C: c, Group: g, Version: v, Resource: r, Decode: decode}
	}
}

func (a *PartialAPI) req(opts metav1.ListOptions) *rest.Request {
	p := "/apis/" + a.Group + "/" + a.Version + "/" + a.Resource
	if a.Group == "" {
		p = "/api/" + a.Version + "/" + a.Resource
	}
	return a.C.Get().AbsPath(p).SetHeader("Accept", "application/json").VersionedParams(&opts, scheme.ParameterCodec)
}

func (a *PartialAPI) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	b, err := a.req(opts).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return DecodePartialList(b, a.Decode)
}

// DecodePartialList decodes a JSON list response (a Status body is returned as an error).
func DecodePartialList(b []byte, decode func([]byte) (runtime.Object, error)) (*PartialList, error) {
	var d struct {
		Kind     string `json:"kind"`
		Metadata struct {
			ResourceVersion    string `json:"resourceVersion"`
			Continue           string `json:"continue"`
			RemainingItemCount *int64 `json:"remainingItemCount"`
		} `json:"metadata"`
		Items   []json.RawMessage `json:"items"`
		Status  string            `json:"status"`
		Message string            `json:"message"`
		Reason  string            `json:"reason"`
		Code    int32             `json:"code"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.Kind == "Status" {
		return nil, &StatusError{metav1.Status{Status: d.Status, Message: d.Message, Reason: metav1.StatusReason(d.Reason), Code: d.Code}}
	}
	out := &PartialList{ListMeta: metav1.ListMeta{ResourceVersion: d.Metadata.ResourceVersion, Continue: d.Metadata.Continue, RemainingItemCount: d.Metadata.RemainingItemCount}}
	out.Items = make([]runtime.Object, 0, len(d.Items))
	for _, raw := range d.Items {
		o, err := decode(raw)
		if err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		out.Items = append(out.Items, o)
	}
	return out, nil
}

func (a *PartialAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	body, err := a.req(opts).Stream(ctx)
	if err != nil {
		return nil, err
	}
	return NewPartialWatch(body, a.Decode), nil
}

// NewPartialWatch turns a JSON watch stream into a watch.Interface of decoded objects (bookmarks become a
// PartialObjectMetadata, errors a *metav1.Status).
func NewPartialWatch(body interface {
	Read([]byte) (int, error)
	Close() error
}, decode func([]byte) (runtime.Object, error)) watch.Interface {
	w := &partialWatch{ch: make(chan watch.Event), done: make(chan struct{}), closer: body}
	go func() {
		defer close(w.ch)
		defer body.Close()
		dec := json.NewDecoder(body)
		for {
			var ev struct {
				Type   string          `json:"type"`
				Object json.RawMessage `json:"object"`
			}
			if err := dec.Decode(&ev); err != nil {
				return
			}
			out, err := DecodePartialEvent(ev.Type, ev.Object, decode)
			if err != nil {
				continue
			}
			select {
			case w.ch <- out:
			case <-w.done:
				return
			}
		}
	}()
	return w
}

// DecodePartialEvent decodes one watch event of a mode B kind.
func DecodePartialEvent(typ string, raw []byte, decode func([]byte) (runtime.Object, error)) (watch.Event, error) {
	switch typ {
	case "ERROR":
		var st metav1.Status
		if err := json.Unmarshal(raw, &st); err != nil {
			return watch.Event{}, err
		}
		return watch.Event{Type: watch.Error, Object: &st}, nil
	case "BOOKMARK":
		var m metav1.PartialObjectMetadata
		if err := json.Unmarshal(raw, &m); err != nil {
			return watch.Event{}, err
		}
		return watch.Event{Type: watch.Bookmark, Object: &m}, nil
	}
	o, err := decode(raw)
	if err != nil {
		return watch.Event{}, err
	}
	return watch.Event{Type: watch.EventType(typ), Object: o}, nil
}

type partialWatch struct {
	ch     chan watch.Event
	done   chan struct{}
	closer interface{ Close() error }
	closed bool
}

func (w *partialWatch) ResultChan() <-chan watch.Event { return w.ch }
func (w *partialWatch) Stop() {
	select {
	case <-w.done:
	default:
		close(w.done)
		_ = w.closer.Close()
	}
}
