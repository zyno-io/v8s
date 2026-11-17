// Package settings persists the per-user settings document.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// ErrInvalid wraps validation failures of a settings document or patch.
var ErrInvalid = errors.New("invalid settings")

// Store holds the settings document and persists it on every change.
type Store struct {
	path string
	// CorruptMovedTo is where an unreadable settings file was renamed to at Open ("" = none).
	CorruptMovedTo string
	// MaxBytes caps the serialized document (0 = unlimited); hosted mode sets it so one user cannot grow the
	// shared server's memory and disk without bound.
	MaxBytes int

	mu  sync.Mutex // serializes Patch and guards cur
	cur protocol.Settings

	cbMu sync.RWMutex
	cbs  []func(protocol.Settings)
}

// DefaultPath returns os.UserConfigDir()/v8s/settings.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "v8s", "settings.json"), nil
}

func defaults() protocol.Settings {
	return normalize(protocol.Settings{AutoSwitchWrite: true})
}

func normalize(s protocol.Settings) protocol.Settings {
	if s.Groups == nil {
		s.Groups = map[string][]string{}
	}
	if s.Links == nil {
		s.Links = map[string]string{}
	}
	if s.Stars == nil {
		s.Stars = []string{}
	}
	if s.Bookmarks == nil {
		s.Bookmarks = []protocol.Bookmark{}
	}
	if s.Selection == nil {
		s.Selection = []string{}
	}
	if s.ColumnLayouts == nil {
		s.ColumnLayouts = map[string]any{}
	}
	if s.ProblemPrefs == nil {
		s.ProblemPrefs = map[string]any{}
	}
	if s.Acks == nil {
		s.Acks = map[string]any{}
	}
	if s.NodeShell == nil {
		s.NodeShell = map[string]protocol.NodeShellSettings{}
	}
	if s.PortForwards == nil {
		s.PortForwards = []protocol.SavedForward{}
	}
	return s
}

// decode parses a JSON document into Settings, starting from defaults. strict rejects unknown keys.
func decode(b []byte, strict bool) (protocol.Settings, error) {
	s := protocol.Settings{AutoSwitchWrite: true}
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&s); err != nil {
		return protocol.Settings{}, err
	}
	if dec.More() {
		return protocol.Settings{}, errors.New("trailing data")
	}
	return normalize(s), nil
}

func clone(s protocol.Settings) protocol.Settings {
	b, err := json.Marshal(s)
	if err == nil {
		if c, err := decode(b, false); err == nil {
			return c
		}
	}
	return defaults()
}

// Open loads path (missing file = defaults). An empty path means memory-only.
func Open(path string) (*Store, error) {
	st := &Store{path: path, cur: defaults()}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	s, err := decode(b, false)
	if err != nil {
		// A corrupt file must not stop startup: keep it aside and start from defaults.
		aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, aside); rerr == nil {
			st.CorruptMovedTo = aside
		}
		return st, nil
	}
	st.cur = s
	return st, nil
}

// Get returns a deep copy of the document.
func (s *Store) Get() protocol.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.cur)
}

// OnChange registers fn, called (outside the lock) after each successful Patch.
func (s *Store) OnChange(fn func(protocol.Settings)) {
	s.cbMu.Lock()
	s.cbs = append(s.cbs, fn)
	s.cbMu.Unlock()
}

// Patch applies an RFC 7386 merge patch, validates, persists atomically, and returns the new document.
func (s *Store) Patch(patch map[string]any) (protocol.Settings, error) {
	// Normalize the patch to plain JSON types.
	pb, err := json.Marshal(patch)
	if err != nil {
		return protocol.Settings{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var p map[string]any
	if err := json.Unmarshal(pb, &p); err != nil || p == nil {
		if err == nil {
			err = errors.New("patch must be an object")
		}
		return protocol.Settings{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	s.mu.Lock()
	cb, err := json.Marshal(s.cur)
	if err != nil {
		s.mu.Unlock()
		return protocol.Settings{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(cb, &doc); err != nil {
		s.mu.Unlock()
		return protocol.Settings{}, err
	}
	merged := mergePatch(doc, p).(map[string]any)
	mb, err := json.Marshal(merged)
	if err != nil {
		s.mu.Unlock()
		return protocol.Settings{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if s.MaxBytes > 0 && len(mb) > s.MaxBytes {
		s.mu.Unlock()
		return protocol.Settings{}, fmt.Errorf("%w: settings document exceeds %d bytes", ErrInvalid, s.MaxBytes)
	}
	next, err := decode(mb, true)
	if err != nil {
		s.mu.Unlock()
		return protocol.Settings{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := s.persist(next); err != nil {
		s.mu.Unlock()
		return protocol.Settings{}, err
	}
	s.cur = next
	out := clone(next)
	s.mu.Unlock()

	s.cbMu.RLock()
	cbs := append([]func(protocol.Settings){}, s.cbs...)
	s.cbMu.RUnlock()
	for _, fn := range cbs {
		fn(clone(out))
	}
	return out, nil
}

func mergePatch(target any, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

func (s *Store) persist(v protocol.Settings) error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	tmp := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write settings: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
