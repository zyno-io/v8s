package fake

// Extension points for the integrations' fake data (Kyverno, Trivy, Kubescape, platform kinds, problem
// fixtures): each registers its kinds and a generator from an init() in its own file of this package.

var extGen []func(w *World, c *Cluster)

// registerKinds adds fake kinds (served typed, as Table, or as mode B partial JSON) and their discovery entries.
func registerKinds(defs ...*kindDef) {
	for _, d := range defs {
		kindDefs = append(kindDefs, d)
		kindByGVR[d.gvr] = d
	}
}

// registerGen adds a generator run at the end of every fake cluster's world generation. It runs with w.mu
// held: use w.putLocked. c.name is the cluster name; clusterIndex gives the position of the cluster.
func registerGen(f func(w *World, c *Cluster)) { extGen = append(extGen, f) }

// clusterIndex returns the index of a fake cluster name (0 for unknown names).
func clusterIndex(name string) int {
	for i := range clusterNames {
		if clusterNames[i] == name {
			return i
		}
	}
	return 0
}
