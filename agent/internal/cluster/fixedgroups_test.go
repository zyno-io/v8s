package cluster

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// groupedSource gives every cluster a source-provided group, as hosted mode does with displayGroups.
type groupedSource struct{ *fake.Source }

func (g groupedSource) Clusters() []protocol.Cluster {
	cs := g.Source.Clusters()
	for i := range cs {
		cs[i].Groups = []string{"Staging"}
	}
	return cs
}

// Source groups are listed as fixed (read-only in the UI); the user's own groups are merged into Groups only.
func TestListMarksSourceGroupsFixed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, groupedSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 10})}, eng, nil)
	t.Cleanup(m.Close)
	set, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetSettings(set)
	id := m.List().Clusters[0].ID
	if _, err := set.Patch(map[string]any{"groups": map[string]any{"mine": []any{id}}}); err != nil {
		t.Fatal(err)
	}
	c := m.List().Clusters[0]
	if !slices.Equal(c.Groups, []string{"Staging", "mine"}) || !slices.Equal(c.FixedGroups, []string{"Staging"}) {
		t.Fatalf("groups %v, fixed %v", c.Groups, c.FixedGroups)
	}
	plain, _ := newFakeManager(t, fake.Config{Clusters: 1, Pods: 10})
	if fg := plain.List().Clusters[0].FixedGroups; fg != nil {
		t.Fatalf("a source without groups has no fixed groups: %v", fg)
	}
}
