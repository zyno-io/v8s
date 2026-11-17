package ops

import (
	"context"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

var (
	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

// Watch follows a single object on the read context, emitting its state and every change until ctx is done.
func (s *Service) Watch(ctx context.Context, ref protocol.ObjectRef, keepManagedFields, reveal bool, emit func(protocol.ObjectFrame)) error {
	gvr, err := parseGVR(ref.GVR)
	if err != nil {
		return err
	}
	if ref.Name == "" {
		return newErr("bad_request", "name is required")
	}
	c, err := s.conn(ref.Cluster)
	if err != nil {
		return err
	}
	ri := resource(c.Dynamic(), gvr, ref.Namespace)

	send := func(o *unstructured.Unstructured, deleted bool) {
		pl, err := buildPayload(o, keepManagedFields, reveal)
		if err != nil {
			emit(protocol.ObjectFrame{Error: &newErr("internal", "%s", err.Error()).Body})
			return
		}
		emit(protocol.ObjectFrame{ObjectPayload: *pl, Deleted: deleted})
	}

	last, gerr := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if gerr != nil {
		return mapErr(gerr, false)
	}
	send(last, false)
	rv := last.GetResourceVersion()
	deletedSent := false
	backoff := watchBackoffMin

	for ctx.Err() == nil {
		expired := false
		w, werr := ri.Watch(ctx, metav1.ListOptions{
			FieldSelector:       "metadata.name=" + ref.Name,
			ResourceVersion:     rv,
			AllowWatchBookmarks: true,
		})
		if werr == nil {
			got := false
		loop:
			for {
				select {
				case <-ctx.Done():
					w.Stop()
					return nil
				case ev, ok := <-w.ResultChan():
					if !ok {
						break loop
					}
					got = true
					switch ev.Type {
					case watch.Added, watch.Modified:
						if o, ok := ev.Object.(*unstructured.Unstructured); ok {
							last, rv, deletedSent = o, o.GetResourceVersion(), false
							send(o, false)
						}
					case watch.Deleted:
						if o, ok := ev.Object.(*unstructured.Unstructured); ok {
							last, rv, deletedSent = o, o.GetResourceVersion(), true
							send(o, true)
						}
					case watch.Bookmark:
						if o, ok := ev.Object.(*unstructured.Unstructured); ok && o.GetResourceVersion() != "" {
							rv = o.GetResourceVersion()
						}
					case watch.Error:
						st := apierrors.FromObject(ev.Object)
						expired = apierrors.IsResourceExpired(st) || apierrors.IsGone(st)
						break loop
					}
				}
			}
			w.Stop()
			if got {
				backoff = watchBackoffMin
			}
		} else {
			expired = apierrors.IsResourceExpired(werr) || apierrors.IsGone(werr)
		}
		if ctx.Err() != nil {
			return nil
		}
		if expired {
			o, gerr := ri.Get(ctx, ref.Name, metav1.GetOptions{})
			switch {
			case gerr == nil:
				rv = o.GetResourceVersion()
				if rv != last.GetResourceVersion() || deletedSent {
					send(o, false)
				}
				last, deletedSent = o, false
			case apierrors.IsNotFound(gerr):
				if !deletedSent {
					send(last, true)
					deletedSent = true
				}
				rv = ""
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > watchBackoffMax {
			backoff = watchBackoffMax
		}
	}
	return nil
}
