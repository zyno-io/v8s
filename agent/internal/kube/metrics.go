package kube

import (
	"context"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/usage"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// MetricsTimeout bounds one metrics.k8s.io list.
const MetricsTimeout = 20 * time.Second

// MetricsReader is the optional backend capability behind usage columns: LISTs of metrics.k8s.io PodMetrics and
// NodeMetrics (the API has no watch). A Conn that does not implement it has no usage.
type MetricsReader interface {
	// PodMetrics lists the pod usage of one namespace, or of the whole cluster for "".
	PodMetrics(ctx context.Context, namespace string) ([]usage.PodSample, error)
	NodeMetrics(ctx context.Context) ([]usage.NodeSample, error)
}

// MetricsOf returns c's MetricsReader when it has one.
func MetricsOf(c Conn) (MetricsReader, bool) {
	m, ok := c.(MetricsReader)
	return m, ok
}

type metricsClient struct {
	once sync.Once
	c    *metricsclient.Clientset
	err  error
}

// metrics builds the clientset from the cluster's tuned rest.Config on first use, so it shares the protobuf
// content type, the rate limiter (the hosted per-user limiter) and the credential of every other request.
func (c *ClientConn) metrics() (*metricsclient.Clientset, error) {
	c.mc.once.Do(func() { c.mc.c, c.mc.err = metricsclient.NewForConfig(c.cfg) })
	return c.mc.c, c.mc.err
}

// PodMetrics implements MetricsReader.
func (c *ClientConn) PodMetrics(ctx context.Context, namespace string) ([]usage.PodSample, error) {
	mc, err := c.metrics()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, MetricsTimeout)
	defer cancel()
	l, err := mc.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return usage.FromPodMetrics(l), nil
}

// NodeMetrics implements MetricsReader.
func (c *ClientConn) NodeMetrics(ctx context.Context) ([]usage.NodeSample, error) {
	mc, err := c.metrics()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, MetricsTimeout)
	defer cancel()
	l, err := mc.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return usage.FromNodeMetrics(l), nil
}
