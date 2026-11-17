package streams

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
)

const (
	maxLineBytes = 256 * 1024 // longer lines are split
	maxLogPods   = 50         // containers followed by one multi-pod stream
	mergeWait    = 250 * time.Millisecond
)

// LogOpener opens the log of one pod container (kube.Runtime.PodLogs).
type LogOpener func(ctx context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error)

// LogSpec is a resolved log request.
type LogSpec struct {
	Namespace    string
	Targets      []LogTarget // one: single-pod stream (raw lines); several: merged, lines prefixed pod/container\t
	Follow       bool
	TailLines    *int64
	SinceSeconds *int64
	Previous     bool
	Timestamps   bool // the client wants the timestamps kept
}

// LogTarget is one pod container.
type LogTarget struct{ Pod, Container string }

func (s *LogSpec) merged() bool { return len(s.Targets) > 1 }

func (s *LogSpec) options(t LogTarget) *corev1.PodLogOptions {
	o := &corev1.PodLogOptions{Container: t.Container, Follow: s.Follow, Previous: s.Previous, TailLines: s.TailLines, SinceSeconds: s.SinceSeconds,
		Timestamps: s.Timestamps || s.merged()} // merging needs timestamps whether or not the client shows them
	return o
}

// splitTimestamp parses the RFC3339Nano prefix the API server adds with timestamps=true.
func splitTimestamp(line []byte) (time.Time, []byte, bool) {
	i := bytes.IndexByte(line, ' ')
	if i < 20 || i > 40 {
		return time.Time{}, line, false
	}
	ts, err := time.Parse(time.RFC3339Nano, string(line[:i]))
	if err != nil {
		return time.Time{}, line, false
	}
	return ts, line[i+1:], true
}

// readLines splits r into '\n'-terminated lines (a final unterminated line gets one; over-long lines are
// split) and sends them to out until r ends or ctx is done.
func readLines(ctx context.Context, r io.Reader, out func([]byte) bool) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var acc []byte
	for {
		chunk, err := br.ReadSlice('\n')
		acc = append(acc, chunk...)
		switch {
		case err == nil:
			if !out(acc) {
				return nil
			}
			acc = nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(acc) >= maxLineBytes {
				if !out(append(acc, '\n')) {
					return nil
				}
				acc = nil
			}
		default:
			if len(acc) > 0 && !out(append(acc, '\n')) {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
	}
}

// streamLogs produces the output lines of spec into lines (closed at the end) and returns the first error.
// Single pod: raw lines. Several: merged by timestamp, prefixed "<pod>/<container>\t", timestamps kept only
// when the client asked for them.
func streamLogs(ctx context.Context, open LogOpener, spec *LogSpec, lines chan<- []byte) error {
	defer close(lines)
	send := func(b []byte) bool {
		select {
		case lines <- b:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !spec.merged() {
		t := spec.Targets[0]
		rc, err := open(ctx, spec.Namespace, t.Pod, spec.options(t))
		if err != nil {
			return err
		}
		defer rc.Close()
		go func() { <-ctx.Done(); rc.Close() }()
		return readLines(ctx, rc, send)
	}
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srcs := make([]<-chan Line, len(spec.Targets))
	errs := make(chan error, len(spec.Targets))
	for i, t := range spec.Targets {
		ch := make(chan Line, 16)
		srcs[i] = ch
		go func(t LogTarget) {
			defer close(ch)
			rc, err := open(mctx, spec.Namespace, t.Pod, spec.options(t))
			if err != nil {
				errs <- fmt.Errorf("%s/%s: %w", t.Pod, t.Container, err)
				return
			}
			defer rc.Close()
			go func() { <-mctx.Done(); rc.Close() }()
			src := t.Pod + "/" + t.Container
			var last time.Time
			err = readLines(mctx, rc, func(b []byte) bool {
				ts, text, ok := splitTimestamp(b)
				if ok {
					last = ts
				} else {
					ts = last
				}
				select {
				case ch <- Line{TS: ts, Src: src, Text: lineText(b, text, ok, spec.Timestamps)}:
					return true
				case <-mctx.Done():
					return false
				}
			})
			if err != nil && mctx.Err() == nil {
				errs <- fmt.Errorf("%s/%s: %w", t.Pod, t.Container, err)
			}
		}(t)
	}
	err := Merge(mctx, srcs, mergeWait, func(l Line) error {
		out := make([]byte, 0, len(l.Src)+1+len(l.Text))
		out = append(append(append(out, l.Src...), '\t'), l.Text...)
		if !send(out) {
			return context.Cause(ctx)
		}
		return nil
	})
	if err == nil {
		select {
		case err = <-errs:
		default:
		}
	}
	return err
}

// lineText is the text of a merged line: with the timestamp when the client asked for it, without otherwise.
func lineText(raw, text []byte, hadTS, keepTS bool) []byte {
	if keepTS || !hadTS {
		return raw
	}
	return text
}

// DownloadLog writes the whole log of spec (no tail limit unless set) to w, merged like a stream.
func DownloadLog(ctx context.Context, open LogOpener, spec *LogSpec, w io.Writer) error {
	lines := make(chan []byte, 64)
	var serr error
	done := make(chan struct{})
	go func() { serr = streamLogs(ctx, open, spec, lines); close(done) }()
	for l := range lines {
		if _, err := w.Write(l); err != nil {
			return err
		}
	}
	<-done
	return serr
}

// sortTargets orders targets by pod then container (stable merged output).
func sortTargets(ts []LogTarget) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Pod != ts[j].Pod {
			return ts[i].Pod < ts[j].Pod
		}
		return ts[i].Container < ts[j].Container
	})
}

var _ = protocol.InitialLogCredit
