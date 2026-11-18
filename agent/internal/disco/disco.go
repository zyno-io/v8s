// Package disco performs Kubernetes API discovery for one cluster.
package disco

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/client-go/rest"
)

// Resource is one discovered API resource (never a subresource).
type Resource struct {
	Group, Version, Resource, Kind string
	Namespaced                     bool
	ShortNames                     []string
	Verbs                          []string
	// Stale is the aggregated-discovery freshness of the resource's version: its aggregated API server is not answering.
	Stale bool
}

// Placeholder reports whether r stands for a group-version that is listed but not answering (a placeholder with no
// resource name: see the aggregated and legacy parsers). It is never listable and never matches a kind.
func (r Resource) Placeholder() bool { return r.Resource == "" }

// GVR returns "v1/pods" for the core group, "apps/v1/deployments" otherwise.
func (r Resource) GVR() string {
	if r.Group == "" {
		return r.Version + "/" + r.Resource
	}
	return r.Group + "/" + r.Version + "/" + r.Resource
}

// Listable reports whether the verbs include both "list" and "watch".
func (r Resource) Listable() bool {
	var l, w bool
	for _, v := range r.Verbs {
		switch v {
		case "list":
			l = true
		case "watch":
			w = true
		}
	}
	return l && w
}

const aggregatedAccept = "application/json;g=apidiscovery.k8s.io;v=v2;as=APIGroupDiscoveryList, " +
	"application/json;g=apidiscovery.k8s.io;v=v2beta1;as=APIGroupDiscoveryList, application/json"

var errNotAggregated = errors.New("not an aggregated discovery document")

type typeMeta struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
}

type aggList struct {
	typeMeta
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Versions []struct {
			Version   string `json:"version"`
			Freshness string `json:"freshness"`
			Resources []struct {
				Resource     string `json:"resource"`
				ResponseKind struct {
					Kind string `json:"kind"`
				} `json:"responseKind"`
				Scope        string   `json:"scope"`
				ShortNames   []string `json:"shortNames"`
				Verbs        []string `json:"verbs"`
				Subresources []any    `json:"subresources"`
			} `json:"resources"`
		} `json:"versions"`
	} `json:"items"`
}

func blank(b []byte) bool { return len(bytes.TrimSpace(b)) == 0 }

func parseAggBody(b []byte, out *[]Resource, seen map[string]bool) error {
	if blank(b) {
		return nil
	}
	var l aggList
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("decode aggregated discovery: %w", err)
	}
	if l.Kind != "APIGroupDiscoveryList" ||
		(l.APIVersion != "apidiscovery.k8s.io/v2" && l.APIVersion != "apidiscovery.k8s.io/v2beta1") {
		return errNotAggregated
	}
	for _, g := range l.Items {
		group := g.Metadata.Name
		for _, v := range g.Versions { // preference order: first version wins
			if v.Freshness == "Stale" && len(v.Resources) == 0 {
				// listed, but its server is not answering and no resource is known: keep the group-version, so a
				// consumer can tell "down" from "not installed"
				*out = append(*out, Resource{Group: group, Version: v.Version, Stale: true})
			}
			for _, r := range v.Resources {
				if r.Resource == "" || strings.Contains(r.Resource, "/") {
					continue
				}
				key := group + "\x00" + r.Resource
				if seen[key] {
					continue
				}
				seen[key] = true
				*out = append(*out, Resource{
					Group: group, Version: v.Version, Resource: r.Resource, Kind: r.ResponseKind.Kind,
					Namespaced: r.Scope == "Namespaced", ShortNames: r.ShortNames, Verbs: r.Verbs,
					Stale: v.Freshness == "Stale",
				})
			}
		}
	}
	return nil
}

// ParseAggregated parses GET /api and GET /apis bodies in aggregated discovery format. Either may be empty.
func ParseAggregated(core, groups []byte) ([]Resource, error) {
	out := []Resource{}
	seen := map[string]bool{}
	if err := parseAggBody(core, &out, seen); err != nil {
		return nil, err
	}
	if err := parseAggBody(groups, &out, seen); err != nil {
		return nil, err
	}
	sortResources(out)
	return out, nil
}

type apiVersions struct {
	typeMeta
	Versions []string `json:"versions"`
}

type apiGroupList struct {
	typeMeta
	Groups []struct {
		Name     string `json:"name"`
		Versions []struct {
			GroupVersion string `json:"groupVersion"`
			Version      string `json:"version"`
		} `json:"versions"`
		PreferredVersion struct {
			Version string `json:"version"`
		} `json:"preferredVersion"`
	} `json:"groups"`
}

type apiResourceList struct {
	Resources []struct {
		Name       string   `json:"name"`
		Namespaced bool     `json:"namespaced"`
		Kind       string   `json:"kind"`
		Verbs      []string `json:"verbs"`
		ShortNames []string `json:"shortNames"`
	} `json:"resources"`
}

// addLegacy parses one resource list; a nil body is a group-version that failed to answer (kept as a placeholder).
func addLegacy(group, version string, body []byte, out *[]Resource, seen map[string]bool) error {
	if body == nil {
		*out = append(*out, Resource{Group: group, Version: version, Stale: true})
		return nil
	}
	var l apiResourceList
	if err := json.Unmarshal(body, &l); err != nil {
		return fmt.Errorf("decode resource list %s/%s: %w", group, version, err)
	}
	for _, r := range l.Resources {
		if r.Name == "" || strings.Contains(r.Name, "/") {
			continue
		}
		key := group + "\x00" + r.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		*out = append(*out, Resource{
			Group: group, Version: version, Resource: r.Name, Kind: r.Kind,
			Namespaced: r.Namespaced, ShortNames: r.ShortNames, Verbs: r.Verbs,
		})
	}
	return nil
}

// groupVersions returns the legacy version order for a group: preferred first, then the rest as listed.
func legacyKey(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}

// ParseLegacy builds resources from legacy discovery bodies; resourceLists is keyed by "v1" or "group/version".
func ParseLegacy(core, groups []byte, resourceLists map[string][]byte) ([]Resource, error) {
	out := []Resource{}
	seen := map[string]bool{}
	if !blank(core) {
		var v apiVersions
		if err := json.Unmarshal(core, &v); err != nil {
			return nil, fmt.Errorf("decode /api: %w", err)
		}
		for _, ver := range v.Versions {
			if b, ok := resourceLists[legacyKey("", ver)]; ok && (b == nil || !blank(b)) {
				if err := addLegacy("", ver, b, &out, seen); err != nil {
					return nil, err
				}
			}
		}
	}
	if !blank(groups) {
		var gl apiGroupList
		if err := json.Unmarshal(groups, &gl); err != nil {
			return nil, fmt.Errorf("decode /apis: %w", err)
		}
		for _, g := range gl.Groups {
			order := []string{}
			if g.PreferredVersion.Version != "" {
				order = append(order, g.PreferredVersion.Version)
			}
			for _, v := range g.Versions {
				if v.Version != g.PreferredVersion.Version {
					order = append(order, v.Version)
				}
			}
			for _, ver := range order {
				if b, ok := resourceLists[legacyKey(g.Name, ver)]; ok && (b == nil || !blank(b)) {
					if err := addLegacy(g.Name, ver, b, &out, seen); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	sortResources(out)
	return out, nil
}

func sortResources(rs []Resource) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].GVR() < rs[j].GVR() })
}

// Fetch discovers all resources, preferring aggregated discovery and falling back to legacy discovery.
func Fetch(ctx context.Context, c rest.Interface) ([]Resource, error) {
	core, err1 := c.Get().AbsPath("/api").SetHeader("Accept", aggregatedAccept).DoRaw(ctx)
	groups, err2 := c.Get().AbsPath("/apis").SetHeader("Accept", aggregatedAccept).DoRaw(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err1 == nil && err2 == nil {
		if rs, err := ParseAggregated(core, groups); err == nil {
			return rs, nil
		}
	}
	return fetchLegacy(ctx, c)
}

func getJSON(ctx context.Context, c rest.Interface, path string) ([]byte, error) {
	return c.Get().AbsPath(path).SetHeader("Accept", "application/json").DoRaw(ctx)
}

func fetchLegacy(ctx context.Context, c rest.Interface) ([]Resource, error) {
	core, err := getJSON(ctx, c, "/api")
	if err != nil {
		return nil, fmt.Errorf("discover /api: %w", err)
	}
	groups, err := getJSON(ctx, c, "/apis")
	if err != nil {
		return nil, fmt.Errorf("discover /apis: %w", err)
	}
	var cv apiVersions
	if err := json.Unmarshal(core, &cv); err != nil {
		return nil, fmt.Errorf("decode /api: %w", err)
	}
	var gl apiGroupList
	if err := json.Unmarshal(groups, &gl); err != nil {
		return nil, fmt.Errorf("decode /apis: %w", err)
	}
	lists := map[string][]byte{}
	fetch := func(group, version string) error {
		path := "/apis/" + group + "/" + version
		if group == "" {
			path = "/api/" + version
		}
		b, err := getJSON(ctx, c, path)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lists[legacyKey(group, version)] = nil // a broken group (e.g. unavailable APIService): kept as a placeholder
			return nil
		}
		lists[legacyKey(group, version)] = b
		return nil
	}
	for _, v := range cv.Versions {
		if err := fetch("", v); err != nil {
			return nil, err
		}
	}
	for _, g := range gl.Groups {
		// every served version: a resource may exist only in a non-preferred one
		for _, v := range g.Versions {
			if err := fetch(g.Name, v.Version); err != nil {
				return nil, err
			}
		}
	}
	return ParseLegacy(core, groups, lists)
}
