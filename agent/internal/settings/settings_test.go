package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub", "settings.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestDefaults(t *testing.T) {
	s, _ := open(t)
	g := s.Get()
	if !g.AutoSwitchWrite {
		t.Fatal("autoSwitchWrite should default true")
	}
	if g.Groups == nil || g.Links == nil || g.Stars == nil || g.Bookmarks == nil || g.Selection == nil ||
		g.ColumnLayouts == nil || g.ProblemPrefs == nil || g.Acks == nil || g.ProtectedClusters == nil {
		t.Fatalf("nil field: %+v", g)
	}
}

func TestMergePatch(t *testing.T) {
	s, _ := open(t)
	_, err := s.Patch(map[string]any{
		"groups":        map[string]any{"a": []any{"x", "y"}, "b": []any{"z"}},
		"stars":         []any{"v1/pods", "apps/v1/deployments"},
		"columnLayouts": map[string]any{"pods": map[string]any{"w": 1, "h": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.Patch(map[string]any{
		"groups":          map[string]any{"a": nil, "c": []any{"q"}},
		"stars":           []any{"v1/nodes"},
		"autoSwitchWrite": false,
		"columnLayouts":   map[string]any{"pods": map[string]any{"h": nil, "k": "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Groups, map[string][]string{"b": {"z"}, "c": {"q"}}) {
		t.Fatalf("groups %v", g.Groups)
	}
	if !reflect.DeepEqual(g.Stars, []string{"v1/nodes"}) || g.AutoSwitchWrite {
		t.Fatalf("%+v", g)
	}
	if !reflect.DeepEqual(g.ColumnLayouts, map[string]any{"pods": map[string]any{"w": float64(1), "k": "v"}}) {
		t.Fatalf("layouts %v", g.ColumnLayouts)
	}
	// null deletes a top-level key back to default
	g, err = s.Patch(map[string]any{"stars": nil})
	if err != nil || g.Stars == nil || len(g.Stars) != 0 {
		t.Fatalf("%v %+v", err, g.Stars)
	}
}

func TestPersistence(t *testing.T) {
	s, p := open(t)
	if _, err := s.Patch(map[string]any{
		"bookmarks": []any{map[string]any{"name": "n", "url": "u"}},
		"links":     map[string]any{"c1": "w1"},
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Get(), s2.Get()) || len(s2.Get().Bookmarks) != 1 {
		t.Fatalf("%+v", s2.Get())
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode())
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestMissingKeysGetDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"stars":["a"]}`), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	g := s.Get()
	if !g.AutoSwitchWrite || g.Groups == nil || len(g.Stars) != 1 {
		t.Fatalf("%+v", g)
	}
	os.WriteFile(p, []byte(`{"autoSwitchWrite":false}`), 0o600)
	s, _ = Open(p)
	if s.Get().AutoSwitchWrite {
		t.Fatal("explicit false lost")
	}
}

func TestInvalidPatch(t *testing.T) {
	s, p := open(t)
	if _, err := s.Patch(map[string]any{"stars": []any{"a"}}); err != nil {
		t.Fatal(err)
	}
	before := s.Get()
	raw, _ := os.ReadFile(p)
	for _, bad := range []map[string]any{
		{"bogus": 1},
		{"stars": "notarray"},
		{"autoSwitchWrite": "yes"},
		{"groups": map[string]any{"a": "x"}},
		{"bookmarks": []any{map[string]any{"name": 1}}},
	} {
		_, err := s.Patch(bad)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v: err %v", bad, err)
		}
	}
	if !reflect.DeepEqual(before, s.Get()) {
		t.Fatal("state changed")
	}
	raw2, _ := os.ReadFile(p)
	if string(raw) != string(raw2) {
		t.Fatal("file changed")
	}
}

func TestWriteFailureLeavesState(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	s, _ := Open(p)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root ignores perms")
	}
	if _, err := s.Patch(map[string]any{"stars": []any{"a"}}); err == nil {
		t.Fatal("expected error")
	}
	if len(s.Get().Stars) != 0 {
		t.Fatal("state changed")
	}
}

func TestConcurrent(t *testing.T) {
	s, p := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := string(rune('a' + i))
			if _, err := s.Patch(map[string]any{"links": map[string]any{k: "v"}}); err != nil {
				t.Error(err)
			}
			_ = s.Get()
		}(i)
	}
	wg.Wait()
	if len(s.Get().Links) != 20 {
		t.Fatalf("links %d", len(s.Get().Links))
	}
	s2, err := Open(p)
	if err != nil || len(s2.Get().Links) != 20 {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatal(ents)
	}
}

func TestOnChange(t *testing.T) {
	s, _ := Open("")
	var got []protocol.Settings
	var n int
	s.OnChange(func(x protocol.Settings) { got = append(got, x) })
	s.OnChange(func(protocol.Settings) { n++ })
	s.Patch(map[string]any{"bogus": 1})
	if len(got) != 0 {
		t.Fatal("fired on failure")
	}
	s.Patch(map[string]any{"selection": []any{"a"}})
	if len(got) != 1 || n != 1 || got[0].Selection[0] != "a" {
		t.Fatalf("%v %d", got, n)
	}
}

func TestMemoryOnly(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.Patch(map[string]any{"stars": []any{"x"}})
	if err != nil || len(g.Stars) != 1 || len(s.Get().Stars) != 1 {
		t.Fatal(err)
	}
}

func TestCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{not json`), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatalf("a corrupt file must not stop startup: %v", err)
	}
	if !s.Get().AutoSwitchWrite {
		t.Fatal("defaults expected")
	}
	if s.CorruptMovedTo == "" {
		t.Fatal("corrupt file not moved aside")
	}
	b, _ := os.ReadFile(s.CorruptMovedTo)
	if string(b) != `{not json` {
		t.Fatal("original content must be kept aside")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("bad file still in place")
	}
	if _, err := s.Patch(map[string]any{"stars": []any{"x"}}); err != nil {
		t.Fatal(err)
	}
}

func TestGetIsDeepCopy(t *testing.T) {
	s, _ := Open("")
	s.Patch(map[string]any{"groups": map[string]any{"a": []any{"x"}}})
	g := s.Get()
	g.Groups["a"][0] = "mutated"
	g.Groups["z"] = nil
	if s.Get().Groups["a"][0] != "x" || len(s.Get().Groups) != 1 {
		t.Fatal("aliasing")
	}
}
