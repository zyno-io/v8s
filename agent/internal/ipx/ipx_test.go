package ipx

import (
	"fmt"
	"testing"
)

var testField = RegisterField("t.field", "Test", false)

func build(t *testing.T, f func(b *Builder)) []Tok {
	t.Helper()
	var b Builder
	f(&b)
	s := b.Set()
	if s == nil {
		return nil
	}
	return s.T
}

func TestBuilderSpans(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"10.0.0.5/24", "10.0.0.0/24", true},
		{"10.0.0.0/0", "", false},
		{"::/0", "", false},
		{"10.0.0.5/32", "10.0.0.5", true},
		{"fd00::5/128", "fd00::5", true},
		{"fd00::5/64", "fd00::/64", true},
		{"10.0.0.1-10.0.0.9", "10.0.0.1-10.0.0.9", true},
		{"10.0.0.1 - 10.0.0.9", "10.0.0.1-10.0.0.9", true},
		{"10.0.0.1-10.0.0.9/24", "10.0.0.1-10.0.0.9", true},
		{"10.0.0.9-10.0.0.1", "", false},
		{"10.0.0.1-fd00::1", "", false},
		{"::ffff:10.0.0.0/104", "10.0.0.0/8", true},
		{"10.0.0.7", "10.0.0.7", true},
		{"0.0.0.0", "", false},
		{"None", "", false},
		{"", "", false},
		{"nonsense", "", false},
	} {
		ts := build(t, func(b *Builder) { b.Span(c.in, testField) })
		if (len(ts) == 1) != c.ok {
			t.Errorf("Span(%q): %d tokens", c.in, len(ts))
			continue
		}
		if c.ok && ts[0].String() != c.want {
			t.Errorf("Span(%q) = %s, want %s", c.in, ts[0], c.want)
		}
	}
}

func TestBuilderAddrHostPairList(t *testing.T) {
	ts := build(t, func(b *Builder) {
		b.Addr("10.0.0.2", testField)
		b.Addr("None", testField)
		b.Addr("::", testField)
		b.Host("10.0.0.1/24", testField)
		b.Pair("10.0.0.10", "10.0.0.20", testField)
		b.Pair("10.0.0.20", "10.0.0.10", testField)
		b.List("fd00::1, fd00::2,junk,,10.0.0.2", testField)
		b.Addr("10.0.0.2", testField) // duplicate
	})
	var got []string
	for _, tk := range ts {
		got = append(got, tk.String())
	}
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.10-10.0.0.20", "fd00::1", "fd00::2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuilderCap(t *testing.T) {
	ts := build(t, func(b *Builder) {
		for i := 0; i < MaxPerRow+50; i++ {
			b.Addr(fmt.Sprintf("10.%d.%d.1", i/256, i%256), testField)
		}
	})
	if len(ts) != MaxPerRow {
		t.Fatalf("%d tokens", len(ts))
	}
	for i := 1; i < len(ts); i++ {
		if ts[i-1].Lo.Cmp(ts[i].Lo) >= 0 {
			t.Fatal("not sorted")
		}
	}
}

func TestSetAllocs(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() {
		var b Builder
		b.Addr("10.0.0.5", testField)
		if b.Set() == nil {
			t.Fatal("nil")
		}
	}); n != 1 {
		t.Fatalf("1 token: %v allocs", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		var b Builder
		b.Addr("10.0.0.5", testField)
		b.Addr("fd00::5", testField)
		_ = b.Set()
	}); n != 1 {
		t.Fatalf("2 tokens: %v allocs", n)
	}
	var b Builder
	if b.Set() != nil {
		t.Fatal("empty builder must give nil")
	}
}

func TestTokRoundTrip(t *testing.T) {
	for _, s := range []string{"10.0.0.5", "10.0.0.0/24", "fd00::/64", "10.0.0.1-10.0.0.9", "fd00::1"} {
		ts := build(t, func(b *Builder) { b.Span(s, testField) })
		if len(ts) != 1 || ts[0].String() != s {
			t.Errorf("%s -> %v", s, ts)
		}
	}
}
