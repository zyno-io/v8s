package ops

import (
	"context"

	"github.com/zyno-io/v8s/agent/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrReplaced is the message of the conflict returned when a request pinned to an object uid (ObjectRef.UID)
// finds a different incarnation of the object: it was deleted and recreated since the client read it.
const ErrReplaced = "object was replaced"

// VerifyUID checks that the live object still has the uid the client saw. An empty uid checks nothing. A
// different uid is a "conflict" ("object was replaced"); a missing object is not_found.
func VerifyUID(ctx context.Context, c kube.Conn, gvr schema.GroupVersionResource, ns, name, uid string) error {
	if uid == "" {
		return nil
	}
	o, err := resource(c.Dynamic(), gvr, ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return mapErr(err, false)
	}
	if string(o.GetUID()) != uid {
		return newErr("conflict", "%s", ErrReplaced)
	}
	return nil
}

// VerifyRefUID is VerifyUID for a ref of the form used by streams and jobs ("v1/pods"): it reads through the
// cluster's read context.
func (s *Service) VerifyRefUID(ctx context.Context, cluster, gvr, ns, name, uid string) error {
	if uid == "" {
		return nil
	}
	g, err := parseGVR(gvr)
	if err != nil {
		return err
	}
	c, err := s.conn(cluster)
	if err != nil {
		return err
	}
	return VerifyUID(ctx, c, g, ns, name, uid)
}

// replacedErr turns a precondition failure of a uid-pinned write into the "object was replaced" conflict.
func replacedErr(err error, uid string) (error, bool) {
	if uid != "" && apierrors.IsConflict(err) {
		return newErr("conflict", "%s", ErrReplaced), true
	}
	return nil, false
}
