package kube

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// fakeStream is one half of an in-memory stream: the "server" side is driven by the test.
type fakeStream struct {
	r  *io.PipeReader
	w  *io.PipeWriter
	id uint32
}

func (s *fakeStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *fakeStream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *fakeStream) Close() error                { return s.w.Close() }
func (s *fakeStream) Reset() error                { _ = s.r.CloseWithError(io.ErrClosedPipe); return s.w.Close() }
func (s *fakeStream) Headers() http.Header        { return nil }
func (s *fakeStream) Identifier() uint32          { return s.id }

// fakeTunnel plays the API server's side of the portforward protocol.
type fakeTunnel struct {
	mu      sync.Mutex
	headers []http.Header
	// server side of each stream, by creation order: error then data per connection
	toClient []*io.PipeWriter
	fromCli  []*io.PipeReader
	closed   chan bool
	once     sync.Once
}

func (f *fakeTunnel) CreateStream(h http.Header) (httpstream.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, h.Clone())
	cr, sw := io.Pipe() // server → client
	sr, cw := io.Pipe() // client → server
	f.toClient = append(f.toClient, sw)
	f.fromCli = append(f.fromCli, sr)
	return &fakeStream{r: cr, w: cw, id: uint32(len(f.headers))}, nil
}
func (f *fakeTunnel) Close() error                       { f.once.Do(func() { close(f.closed) }); return nil }
func (f *fakeTunnel) CloseChan() <-chan bool             { return f.closed }
func (f *fakeTunnel) SetIdleTimeout(time.Duration)       {}
func (f *fakeTunnel) RemoveStreams(...httpstream.Stream) {}

// DialPod's connections are plain streams on one tunnel: no local listener exists to dial, each connection
// carries the portforward headers (unique request id, remote port) and a remote failure surfaces as a read error.
func TestPodDialerUsesTunnelStreams(t *testing.T) {
	tun := &fakeTunnel{closed: make(chan bool)}
	pd := newPodDialer(tun, "kube-system", "hubble-relay-x", 4245)
	defer pd.Close()
	c1, err := pd.DialContext(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := pd.DialContext(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if len(tun.headers) != 4 {
		t.Fatalf("want an error and a data stream per connection, got %d streams", len(tun.headers))
	}
	if tun.headers[0].Get(corev1.StreamType) != corev1.StreamTypeError || tun.headers[1].Get(corev1.StreamType) != corev1.StreamTypeData ||
		tun.headers[1].Get(corev1.PortHeader) != "4245" {
		t.Fatalf("headers: %v", tun.headers)
	}
	if tun.headers[1].Get(corev1.PortForwardRequestIDHeader) == tun.headers[3].Get(corev1.PortForwardRequestIDHeader) {
		t.Fatal("connections share a request id")
	}
	// data flows both ways over the data stream
	go func() { _, _ = tun.toClient[1].Write([]byte("pong")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c1, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("read %q %v", buf, err)
	}
	go func() { _, _ = c1.Write([]byte("ping")) }()
	if _, err := io.ReadFull(tun.fromCli[1], buf); err != nil || string(buf) != "ping" {
		t.Fatalf("write %q %v", buf, err)
	}
	// the pod refuses the connection: reported on the error stream, the reader fails with that message
	go func() { _, _ = tun.toClient[2].Write([]byte("connection refused")); _ = tun.toClient[2].Close() }()
	_, err = c2.Read(buf)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("remote error not surfaced: %v", err)
	}
	// closing the tunnel ends the dialer
	pd.Close()
	select {
	case <-pd.Done:
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if _, err := pd.DialContext(context.Background(), ""); err == nil {
		t.Fatal("dial on a closed tunnel")
	}
}
