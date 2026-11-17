package streams

import (
	"context"
	"fmt"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var debugWait = 90 * time.Second

func (m *Manager) openDebug(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	ref, err := podRefOf(p)
	if err != nil {
		return res, err
	}
	if p.Image == "" {
		return res, ops.NewError("bad_request", "image is required")
	}
	target := p.Target
	if target == "" {
		target = p.Container
	}
	routed, rt, err := m.route(ctx, ref.Cluster, "patch", podsGVR, "ephemeralcontainers", ref.Namespace, ref.Name)
	if err != nil {
		return res, err
	}
	pods := rt.Clientset().CoreV1().Pods(ref.Namespace)
	if target == "" {
		pod, gerr := pods.Get(ctx, ref.Name, metav1.GetOptions{})
		if gerr != nil {
			return res, m.mapRouted(ctx, gerr, routed, "get", podsGVR, "", ref.Namespace, ref.Name)
		}
		target = defaultContainer(pod)
	}
	name := "v8s-debug-" + randSuffix(5)
	image, via, used := p.Image, routed.ViaWrite, routed.ContextID
	res.UsedContext = used
	res.StreamID = m.add(&pendingStream{typ: "debug", run: func(ctx context.Context, t Transport) {
		lctx, lim := m.limitsFor(ctx, via)
		defer lim.Stop()
		s := &interactive{t: t, touch: lim.Touch}
		if _, perr := pods.Patch(lctx, ref.Name, types.StrategicMergePatchType, debugPatch(name, image, target), metav1.PatchOptions{FieldManager: "v8s"}, "ephemeralcontainers"); perr != nil {
			_ = s.finish(lctx, fmt.Errorf("could not add the debug container: %s", m.mapRouted(lctx, perr, routed, "patch", podsGVR, "ephemeralcontainers", ref.Namespace, ref.Name).Error()))
			return
		}
		if werr := waitEphemeral(lctx, func() (*corev1.Pod, error) { return pods.Get(lctx, ref.Name, metav1.GetOptions{}) }, name); werr != nil {
			_ = s.finish(lctx, werr)
			return
		}
		// Attach to the ephemeral container's own process (like kubectl debug): the image's shell is its
		// entrypoint, so the container ends with the session instead of idling behind a second exec'd shell.
		runner := execRunner(rt, ref.Namespace, ref.Name, name, true, true)
		_ = s.runOnce(lctx, [][]string{{"attach"}}, protocol.StreamReady{UsedContext: used, Container: name}, runner)
	}})
	return res, nil
}

// waitEphemeral waits until the ephemeral container is running.
func waitEphemeral(ctx context.Context, get func() (*corev1.Pod, error), name string) error {
	deadline := time.Now().Add(debugWait)
	for {
		pod, err := get()
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			for _, st := range pod.Status.EphemeralContainerStatuses {
				if st.Name != name {
					continue
				}
				if st.State.Running != nil {
					return nil
				}
				if w := st.State.Waiting; w != nil && (w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff" || w.Reason == "InvalidImageName") {
					return fmt.Errorf("debug container image: %s: %s", w.Reason, w.Message)
				}
				if st.State.Terminated != nil {
					return fmt.Errorf("debug container exited: %s", st.State.Terminated.Reason)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("debug container %s did not start within %s", name, debugWait)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
