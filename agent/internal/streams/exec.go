package streams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/client-go/tools/remotecommand"
)

// DefaultShells is the shell discovery order for exec without a command.
var DefaultShells = []string{"/bin/bash", "/bin/sh", "/busybox/sh"}

// Runner runs one command attempt with the session's stdin/stdout (kube.Runtime.Exec / Attach adapters).
type Runner func(ctx context.Context, cmd []string, in io.Reader, out, errw io.Writer, resize remotecommand.TerminalSizeQueue) error

// sizeQueue is a TerminalSizeQueue fed by resize frames.
type sizeQueue struct {
	ch   chan remotecommand.TerminalSize
	done <-chan struct{}
}

func newSizeQueue(done <-chan struct{}) *sizeQueue {
	return &sizeQueue{ch: make(chan remotecommand.TerminalSize, 8), done: done}
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.ch:
		return &s
	case <-q.done:
		return nil
	}
}

func (q *sizeQueue) push(cols, rows int) {
	if cols <= 0 || rows <= 0 || cols > 10000 || rows > 10000 {
		return
	}
	s := remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	for {
		select {
		case q.ch <- s:
			return
		default:
			select { // drop the oldest: only the latest size matters
			case <-q.ch:
			default:
			}
		}
	}
}

// notFound reports whether an exec failure means the binary does not exist in the container, so the next
// shell candidate is worth trying.
func notFound(err error) bool {
	if err == nil {
		return false
	}
	if code, ok := kube.ExitCode(err); ok && (code == 126 || code == 127) {
		return true
	}
	m := strings.ToLower(err.Error())
	for _, s := range []string{"executable file not found", "no such file or directory", "oci runtime exec failed", "not found in $path", "exec: \"", "command terminated with exit code 126", "command terminated with exit code 127"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// DiscoverShell runs the candidates in order until one starts: an attempt that fails because the binary does
// not exist, before producing any output, moves on to the next. It returns the shell that ran (or the last
// candidate tried) and the final error of the session.
func DiscoverShell(ctx context.Context, candidates [][]string, run func(cmd []string) (produced bool, err error), onStart func(cmd []string)) ([]string, error) {
	var last []string
	var lastErr error
	for _, c := range candidates {
		last = c
		if onStart != nil {
			onStart(c)
		}
		produced, err := run(c)
		if err == nil || produced || !notFound(err) || ctx.Err() != nil {
			return c, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no shell found")
	}
	return last, lastErr
}

// interactive bridges a Transport to a command run: client binary frames [0x00][stdin], text {"resize"}; agent
// frames [0x01][stdout], [0x02][stderr], text {"exit": code, "error"?}. touch is called on traffic (idle timer).
type interactive struct {
	t     Transport
	touch func()
}

func (s *interactive) sendJSON(ctx context.Context, m protocol.StreamServerMsg) error {
	b, _ := json.Marshal(m)
	return s.t.Write(ctx, false, b)
}

type frameWriter struct {
	ctx   context.Context
	s     *interactive
	ch    byte
	mu    *sync.Mutex
	wrote *bool
}

func (w *frameWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		chunk := p
		if len(chunk) > protocol.LogBatchBytes {
			chunk = p[:protocol.LogBatchBytes]
		}
		buf := make([]byte, 1+len(chunk))
		buf[0] = w.ch
		copy(buf[1:], chunk)
		if err := w.s.t.Write(w.ctx, true, buf); err != nil {
			return 0, err
		}
		w.mu.Lock()
		*w.wrote = true
		w.mu.Unlock()
		w.s.touch()
		p = p[len(chunk):]
	}
	return n, nil
}

// run executes the session: shell discovery over candidates (or the single given command), then ends the
// stream with the exit frame. ready is sent before the first attempt and again when discovery moves on.
func (s *interactive) run(ctx context.Context, candidates [][]string, ready protocol.StreamReady, runner Runner) error {
	return s.finish(ctx, s.runCore(ctx, candidates, ready, runner, true))
}

func (s *interactive) runCore(ctx context.Context, candidates [][]string, ready protocol.StreamReady, runner Runner, _ bool) error {
	pr, pw := io.Pipe()
	size := newSizeQueue(ctx.Done())
	// client -> agent
	go func() {
		defer pw.Close()
		for {
			bin, data, err := s.t.Read(ctx)
			if err != nil {
				return
			}
			s.touch()
			if bin {
				if len(data) > 1 && data[0] == protocol.StreamChanStdin {
					if _, err := pw.Write(data[1:]); err != nil {
						return
					}
				}
				continue
			}
			var m protocol.StreamClientMsg
			if json.Unmarshal(data, &m) == nil && m.Resize != nil {
				size.push(m.Resize.Cols, m.Resize.Rows)
			}
		}
	}()
	var mu sync.Mutex
	produced := false
	out := &frameWriter{ctx: ctx, s: s, ch: protocol.StreamChanStdout, mu: &mu, wrote: &produced}
	errw := &frameWriter{ctx: ctx, s: s, ch: protocol.StreamChanStderr, mu: &mu, wrote: &produced}
	_, err := DiscoverShell(ctx, candidates, func(cmd []string) (bool, error) {
		mu.Lock()
		produced = false
		mu.Unlock()
		e := runner(ctx, cmd, pr, out, errw, size)
		mu.Lock()
		defer mu.Unlock()
		return produced, e
	}, func(cmd []string) {
		r := ready
		if len(candidates) > 1 || ready.Shell == "" && len(cmd) == 1 {
			r.Shell = cmd[0]
		}
		_ = s.sendJSON(ctx, protocol.StreamServerMsg{Ready: &r})
	})
	return err
}

// runOnce is run for a session without shell discovery.
func (s *interactive) runOnce(ctx context.Context, candidates [][]string, ready protocol.StreamReady, runner Runner) error {
	return s.runCore(ctx, candidates, ready, runner, false)
}

// finish2 ends the stream for an error returned by a session body that did not itself send the exit frame.
func (s *interactive) finish2(ctx context.Context, err error) error { return s.finish(ctx, err) }

// finish sends the exit frame for err (or the limit that cancelled the session) and closes the stream.
func (s *interactive) finish(ctx context.Context, err error) error {
	msg := protocol.StreamServerMsg{}
	code := 0
	switch {
	case context.Cause(ctx) == ErrIdle || context.Cause(ctx) == ErrCap:
		code, msg.Error = 1, context.Cause(ctx).Error()
	case err == nil:
	default:
		if c, ok := kube.ExitCode(err); ok {
			code = c
			if c != 0 && !strings.Contains(err.Error(), "exit code") {
				msg.Error = err.Error()
			}
		} else {
			code, msg.Error = 1, err.Error()
		}
	}
	msg.Exit = &code
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.sendJSON(wctx, msg)
	s.t.Close(CloseNormal, "")
	return err
}
