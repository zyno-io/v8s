package ipx

import (
	"encoding/json"
	"net/netip"
	"os"
	"testing"
)

type vector struct {
	In   string `json:"in"`
	IP   bool   `json:"ip"`
	Mode string `json:"mode"`
	Norm string `json:"norm"`
}

func TestDetectVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/detect.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 30 {
		t.Fatalf("only %d vectors", len(vs))
	}
	for _, v := range vs {
		q, ok := Parse(v.In)
		if ok != v.IP {
			t.Errorf("Parse(%q) ip=%v, want %v", v.In, ok, v.IP)
			continue
		}
		if !ok {
			continue
		}
		if q.Mode.String() != v.Mode || q.Text != v.Norm {
			t.Errorf("Parse(%q) = %s %q, want %s %q", v.In, q.Mode, q.Text, v.Mode, v.Norm)
		}
	}
}

func TestParseLongRejected(t *testing.T) {
	long := ""
	for i := 0; i < 101; i++ {
		long += "1"
	}
	if _, ok := Parse(long); ok {
		t.Fatal("long input accepted")
	}
}

func TestStrong(t *testing.T) {
	for in, want := range map[string]bool{
		"10.0.0.1": true, "10.0.0.0/8": true, "10.0.0.1-10.0.0.2": true, "fd00::/8": true,
		"1.": false, "1.2": false, "10.42.": true, "10.42.3": true, "fd00::": false, "fd00:10:": true, "fd00:10:a": true,
	} {
		q, ok := Parse(in)
		if !ok {
			t.Fatalf("%q not parsed", in)
		}
		if q.Strong() != want {
			t.Errorf("Strong(%q) = %v, want %v", in, q.Strong(), want)
		}
	}
}

func TestPartialSpans(t *testing.T) {
	for in, want := range map[string]int{"10.4": 2, "10.2": 3, "10.0": 1, "10.": 1, "10.42.": 1, "fd00:10:1": 4, "fd00:10:": 1, "fd00:10:1234": 1, "fd00:10:0": 1} {
		q, ok := Parse(in)
		if !ok {
			t.Fatalf("%q not parsed", in)
		}
		if int(q.n) != want {
			t.Errorf("%q: %d spans, want %d", in, q.n, want)
		}
	}
	q, _ := Parse("10.4")
	a := func(s string) Tok { u := FromAddr(netip.MustParseAddr(s)); return Tok{Lo: u, Hi: u, Flags: FlagV4} }
	for s, want := range map[string]Match{"10.4.0.1": MatchPrefix, "10.45.9.9": MatchPrefix, "10.5.0.1": MatchNone, "10.40.0.0": MatchPrefix} {
		tk := a(s)
		if got := q.Match(&tk); got != want {
			t.Errorf("10.4 vs %s = %s, want %s", s, got, want)
		}
	}
	q, _ = Parse("10.2")
	tk := a("10.255.1.1")
	if q.Match(&tk) != MatchPrefix {
		t.Error("10.2 should reach 10.255")
	}
}

func tokOf(t *testing.T, s string, fl ...Flags) Tok {
	t.Helper()
	var b Builder
	if !b.Span(s, 0) {
		t.Fatalf("span %q", s)
	}
	return b.Set().T[0]
}

func TestMatchMatrix(t *testing.T) {
	cases := []struct {
		q, tok string
		want   Match
	}{
		{"10.0.0.5", "10.0.0.5", MatchExact},
		{"10.0.0.5", "10.0.0.0/24", MatchContains},
		{"10.0.0.5", "10.0.0.1-10.0.0.9", MatchContains},
		{"10.0.0.5", "10.0.1.0/24", MatchNone},
		{"10.0.0.5", "10.0.0.6", MatchNone},
		{"10.0.0.0/24", "10.0.0.0/24", MatchExact},
		{"10.0.0.0/24", "10.0.0.7", MatchWithin},
		{"10.0.0.0/24", "10.0.0.128/25", MatchWithin},
		{"10.0.0.0/24", "10.0.0.0/16", MatchContains},
		{"10.0.0.0/24", "10.0.0.200-10.0.1.9", MatchOverlap},
		{"10.0.0.0/24", "10.0.1.7", MatchNone},
		{"10.0.0.1-10.0.0.9", "10.0.0.1-10.0.0.9", MatchExact},
		{"10.0.0.1-10.0.0.9", "10.0.0.5", MatchWithin},
		{"10.0.0.1-10.0.0.9", "10.0.0.0/24", MatchContains},
		{"10.0.0.1-10.0.0.9", "10.0.0.8-10.0.0.20", MatchOverlap},
		{"10.4", "10.4.0.7", MatchPrefix},
		{"10.4", "10.4.0.0/16", MatchContains},
		{"10.4", "10.0.0.0/8", MatchContains},
		{"10.4", "10.40.0.0/24", MatchPrefix},
		{"10.4", "11.4.0.1", MatchNone},
		{"fd00::5", "fd00::5", MatchExact},
		{"fd00::5", "fd00::/64", MatchContains},
		{"fd00:10:", "fd00:10::1", MatchPrefix},
		{"fd00:10:", "fd00::/16", MatchContains},
		{"fd00::5", "10.0.0.5", MatchNone},
		{"10.0.0.5", "fd00::5", MatchNone},
		{"::ffff:10.0.0.5", "10.0.0.5", MatchExact}, // normalization: v4-mapped is v4
		{"10.0.0.5", "::ffff:10.0.0.5", MatchExact},
		{"10.0.0.0/24", "fd00::/8", MatchNone},
	}
	for _, c := range cases {
		q, ok := Parse(c.q)
		if !ok {
			t.Fatalf("query %q", c.q)
		}
		tk := tokOf(t, c.tok)
		if got := q.Match(&tk); got != c.want {
			t.Errorf("%s vs %s = %s, want %s", c.q, c.tok, got, c.want)
		}
	}
}

func TestRankOrder(t *testing.T) {
	q, _ := Parse("10.0.0.5")
	sec := RegisterField("t.secondary", "S", true)
	exact, wide, narrow := tokOf(t, "10.0.0.5"), tokOf(t, "10.0.0.0/8"), tokOf(t, "10.0.0.0/24")
	secTok := exact
	secTok.Field = sec
	r := func(tk Tok) uint32 { m := q.Match(&tk); return RankOf(m, &tk) }
	if !(r(exact) < r(secTok) && r(secTok) < r(narrow) && r(narrow) < r(wide)) {
		t.Fatalf("rank order wrong: exact %d secondary %d narrow %d wide %d", r(exact), r(secTok), r(narrow), r(wide))
	}
	qc, _ := Parse("10.0.0.0/24")
	within, ov, pfx := tokOf(t, "10.0.0.9"), tokOf(t, "10.0.0.200-10.0.1.9"), tokOf(t, "10.0.0.9")
	qp, _ := Parse("10.0")
	rc := func(q *Query, tk Tok) uint32 { return RankOf(q.Match(&tk), &tk) }
	if !(rc(qc, within) == rc(qc, ov)) || !(rc(qc, within) < rc(qp, pfx)) {
		t.Fatalf("tiers wrong")
	}
}

func TestBestPicksTopRanked(t *testing.T) {
	q, _ := Parse("10.0.0.5")
	ts := []Tok{tokOf(t, "10.0.0.0/8"), tokOf(t, "10.0.0.5"), tokOf(t, "10.0.0.0/24")}
	m, i := q.Best(ts)
	if m != MatchExact || i != 1 {
		t.Fatalf("best = %s %d", m, i)
	}
	if m, i := q.Best([]Tok{tokOf(t, "11.0.0.0/8")}); m != MatchNone || i != -1 {
		t.Fatalf("none = %s %d", m, i)
	}
	if !q.Any(ts) || q.Any(ts[:0]) {
		t.Fatal("Any")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"10.0.0.1", "10.", "fd00::", "[fd00::1]:443", "10.0.0.0/8", "a-b", "1.2.3.4-1.2.3.9", "::", "::-::", "fe80::1%eth0",
		"fd00:10:a", "10.0.0.1:80", "::ffff:1.2.3.4/104", "0.0.0.0/0", "[", "%", "-", "/", "1.2.3.", ":::"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, ok := Parse(s)
		if !ok {
			return
		}
		if q.n == 0 || q.n > 4 {
			t.Fatalf("%q: %d spans", s, q.n)
		}
		for i := uint8(0); i < q.n; i++ {
			if q.spans[i].Hi.Cmp(q.spans[i].Lo) < 0 {
				t.Fatalf("%q: span %d reversed", s, i)
			}
		}
		q2, ok := Parse(q.Text)
		if !ok {
			t.Fatalf("Parse(%q) ok but its Text %q does not parse", s, q.Text)
		}
		if q2.Text != q.Text || q2.Mode != q.Mode {
			t.Fatalf("Parse(%q): %s %q; reparsed %s %q", s, q.Mode, q.Text, q2.Mode, q2.Text)
		}
		var tk Tok
		_ = q.Match(&tk)
	})
}
