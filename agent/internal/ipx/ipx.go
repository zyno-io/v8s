// Package ipx holds the IP address machinery of the agent: compact 128-bit tokens that rows carry, a builder
// that fills them at projection time, and the query parser and matcher behind IP search (query.go).
//
// Every address is unmapped and stored as 128-bit big-endian, so IPv4 is ::ffff:a.b.c.d. A CIDR or an a-b range
// is an inclusive [Lo,Hi] span. Tokens only match queries of the same family. The package is a leaf: it imports
// nothing from the project.
package ipx

import (
	"encoding/binary"
	"math/bits"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// U128 is a 128-bit unsigned integer (big-endian order: Hi is the most significant half).
type U128 struct{ Hi, Lo uint64 }

// FromAddr returns a as 128-bit big-endian; an IPv4-mapped address is unmapped first, so v4 is ::ffff:a.b.c.d.
func FromAddr(a netip.Addr) U128 {
	b := a.Unmap().As16()
	return U128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

// Cmp compares u and v.
func (u U128) Cmp(v U128) int {
	switch {
	case u.Hi < v.Hi:
		return -1
	case u.Hi > v.Hi:
		return 1
	case u.Lo < v.Lo:
		return -1
	case u.Lo > v.Lo:
		return 1
	}
	return 0
}

// Addr converts back to an address; v4 selects the IPv4 form.
func (u U128) Addr(v4 bool) netip.Addr {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], u.Hi)
	binary.BigEndian.PutUint64(b[8:], u.Lo)
	if v4 {
		return netip.AddrFrom4([4]byte(b[12:]))
	}
	return netip.AddrFrom16(b)
}

func (u U128) or(v U128) U128 { return U128{u.Hi | v.Hi, u.Lo | v.Lo} }

func (u U128) sub(v U128) U128 {
	lo, borrow := bits.Sub64(u.Lo, v.Lo, 0)
	hi, _ := bits.Sub64(u.Hi, v.Hi, borrow)
	return U128{hi, lo}
}

func (u U128) bitLen() int {
	if u.Hi != 0 {
		return 64 + bits.Len64(u.Hi)
	}
	return bits.Len64(u.Lo)
}

// le reports u <= v.
func (u U128) le(v U128) bool { return u.Cmp(v) <= 0 }

// hostMask has the low hostBits bits set.
func hostMask(hostBits int) U128 {
	switch {
	case hostBits <= 0:
		return U128{}
	case hostBits >= 128:
		return U128{^uint64(0), ^uint64(0)}
	case hostBits >= 64:
		return U128{Hi: uint64(1)<<(hostBits-64) - 1, Lo: ^uint64(0)}
	}
	return U128{Lo: uint64(1)<<hostBits - 1}
}

// v4Base is the low half of ::ffff:0.0.0.0.
const v4Base = uint64(0xffff) << 32

func fromV4(v uint32) U128 { return U128{Lo: v4Base | uint64(v)} }

// Flags describe a token.
type Flags uint8

const (
	FlagV4    Flags = 1 << iota // the token is IPv4
	FlagCIDR                    // the span came from a CIDR (Bits is its prefix length)
	FlagRange                   // the span came from an a-b range
)

// Field names where on an object a token was found. Fields are registered at init and are process-wide.
type Field uint8

type fieldInfo struct {
	path, label string
	secondary   bool
}

var fields = []fieldInfo{{}} // index 0: unknown

// RegisterField registers a field (init time only). Secondary fields rank after primary ones of the same match tier.
func RegisterField(path, label string, secondary bool) Field {
	if len(fields) > 255 {
		panic("ipx: too many fields")
	}
	fields = append(fields, fieldInfo{path, label, secondary})
	return Field(len(fields) - 1)
}

func (f Field) info() fieldInfo {
	if int(f) < len(fields) {
		return fields[f]
	}
	return fieldInfo{}
}

// Path is the object path the token came from ("status.podIPs").
func (f Field) Path() string { return f.info().path }

// Label is the human label ("Pod IP").
func (f Field) Label() string { return f.info().label }

// Secondary reports whether the field ranks below primary fields.
func (f Field) Secondary() bool { return f.info().secondary }

// Tok is one address or span an object holds: 40 bytes.
type Tok struct {
	Lo, Hi U128
	Field  Field
	Flags  Flags
	Bits   uint8 // prefix length of a CIDR span, in the token's own family
}

// V4 reports whether the token is IPv4.
func (t Tok) V4() bool { return t.Flags&FlagV4 != 0 }

// Single reports whether the token is one address (not a CIDR or a range).
func (t Tok) Single() bool { return t.Lo == t.Hi && t.Flags&(FlagCIDR|FlagRange) == 0 }

// String formats the token: "10.0.0.5", "10.0.0.0/24" or "a-b".
func (t Tok) String() string {
	v4 := t.V4()
	switch {
	case t.Flags&FlagCIDR != 0:
		return t.Lo.Addr(v4).String() + "/" + strconv.Itoa(int(t.Bits))
	case t.Lo != t.Hi || t.Flags&FlagRange != 0:
		return t.Lo.Addr(v4).String() + "-" + t.Hi.Addr(v4).String()
	}
	return t.Lo.Addr(v4).String()
}

// Set is the immutable, sorted, de-duplicated tokens of one row.
type Set struct{ T []Tok }

// MaxPerRow caps the tokens one row keeps.
const MaxPerRow = 1024

// Builder collects tokens on the stack (the first two; more spill to the heap); Set copies them into one
// allocation. The zero Builder is ready.
type Builder struct {
	n    int
	buf  [2]Tok
	over []Tok
}

func (b *Builder) add(t Tok) bool {
	if b.n+len(b.over) >= MaxPerRow {
		return false
	}
	if b.n < len(b.buf) {
		b.buf[b.n] = t
		b.n++
	} else {
		b.over = append(b.over, t)
	}
	return true
}

// Addr adds one address; "", "None", the unspecified address and anything unparsable are skipped.
func (b *Builder) Addr(s string, f Field) bool {
	if v, ok := parseV4Fast(s); ok { // the common case, without netip
		if v == 0 {
			return false
		}
		u := fromV4(v)
		return b.add(Tok{Lo: u, Hi: u, Field: f, Flags: FlagV4})
	}
	a, ok := parseAddr(s)
	if !ok || a.IsUnspecified() {
		return false
	}
	var fl Flags
	if a.Is4() {
		fl = FlagV4
	}
	u := FromAddr(a)
	return b.add(Tok{Lo: u, Hi: u, Field: f, Flags: fl})
}

// parseV4Fast parses a canonical dotted quad (no leading zeros, no spaces); anything else reports false.
func parseV4Fast(s string) (uint32, bool) {
	if len(s) < 7 || len(s) > 15 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 4; i++ {
		n, digits := 0, 0
		zero := len(s) > 0 && s[0] == '0'
		for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			n = n*10 + int(s[0]-'0')
			s = s[1:]
			digits++
			if digits > 3 {
				return 0, false
			}
		}
		if digits == 0 || n > 255 || (zero && digits > 1) { // leading zero: let netip reject it
			return 0, false
		}
		v = v<<8 | uint32(n)
		if i < 3 {
			if len(s) == 0 || s[0] != '.' {
				return 0, false
			}
			s = s[1:]
		}
	}
	return v, len(s) == 0
}

// Host adds the address part of "10.0.0.5/24" (a plain address works too).
func (b *Builder) Host(s string, f Field) bool {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return b.Addr(s, f)
}

// Span adds an address, a CIDR (masked; /0 dropped; full length becomes a single address) or a range
// ("a-b", "a - b", "a-b/n").
func (b *Builder) Span(s string, f Field) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 100 {
		return false
	}
	sp, ok := parseSpan(stripZone(s), false)
	if !ok || (sp.lo == sp.hi && sp.lo.Addr(sp.flags&FlagV4 != 0).IsUnspecified()) {
		return false
	}
	return b.add(Tok{Lo: sp.lo, Hi: sp.hi, Field: f, Flags: sp.flags, Bits: sp.bits})
}

// Pair adds the inclusive range lo..hi (both addresses, same family, lo <= hi).
func (b *Builder) Pair(lo, hi string, f Field) bool {
	l, ok1 := parseAddr(lo)
	h, ok2 := parseAddr(hi)
	if !ok1 || !ok2 || l.Is4() != h.Is4() {
		return false
	}
	lu, hu := FromAddr(l), FromAddr(h)
	if lu.Cmp(hu) > 0 {
		return false
	}
	fl := FlagRange
	if l.Is4() {
		fl |= FlagV4
	}
	if lu == hu {
		fl &^= FlagRange
	}
	return b.add(Tok{Lo: lu, Hi: hu, Field: f, Flags: fl})
}

// List adds every address of a comma-separated list.
func (b *Builder) List(csv string, f Field) {
	for csv != "" {
		var one string
		one, csv, _ = strings.Cut(csv, ",")
		b.Addr(one, f)
	}
}

// Single returns the one token of a builder that collected exactly one, so a caller can store it inline
// in a larger allocation instead of through Set.
func (b *Builder) Single() (Tok, bool) {
	if b.n == 1 && len(b.over) == 0 {
		return b.buf[0], true
	}
	return Tok{}, false
}

// Set returns the collected tokens sorted by (Lo, Hi, Field) and de-duplicated, or nil when there are none.
// One token or two share the allocation of the Set.
func (b *Builder) Set() *Set {
	if b.n+len(b.over) == 0 {
		return nil
	}
	// Two paths so the stack buffer never flows into the result (it would escape and cost an allocation).
	if len(b.over) == 0 {
		all := normalize(b.buf[:b.n])
		if s := smallSet(all); s != nil {
			return s
		}
		return &Set{T: slices.Clone(all)}
	}
	all := normalize(append(slices.Grow(slices.Clone(b.buf[:b.n]), len(b.over)), b.over...))
	if s := smallSet(all); s != nil {
		return s
	}
	return &Set{T: all}
}

// normalize sorts by (Lo, Hi, Field) and drops duplicates, in place.
func normalize(all []Tok) []Tok {
	if len(all) < 2 {
		return all
	}
	slices.SortFunc(all, func(x, y Tok) int {
		if c := x.Lo.Cmp(y.Lo); c != 0 {
			return c
		}
		if c := x.Hi.Cmp(y.Hi); c != 0 {
			return c
		}
		return int(x.Field) - int(y.Field)
	})
	return slices.CompactFunc(all, func(x, y Tok) bool { return x.Lo == y.Lo && x.Hi == y.Hi && x.Field == y.Field })
}

// smallSet copies one or two tokens into one allocation with the Set; nil for more.
func smallSet(all []Tok) *Set {
	switch len(all) {
	case 1:
		x := &struct {
			Set
			t [1]Tok
		}{}
		x.t[0] = all[0]
		x.T = x.t[:]
		return &x.Set
	case 2:
		x := &struct {
			Set
			t [2]Tok
		}{}
		copy(x.t[:], all)
		x.T = x.t[:]
		return &x.Set
	}
	return nil
}

// ---- parsing helpers shared by the Builder and the query parser -----------------------------

func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "None" {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// stripZone removes a "%zone" (it may sit before a "/n").
func stripZone(s string) string {
	i := strings.IndexByte(s, '%')
	if i < 0 {
		return s
	}
	if j := strings.IndexByte(s[i:], '/'); j >= 0 {
		return s[:i] + s[i+j:]
	}
	return s[:i]
}

type span struct {
	lo, hi U128
	flags  Flags
	bits   uint8
}

// parseSpan parses an address, a CIDR or an a-b range. Zones are already stripped. Queries keep /0
// (allowZero); stored tokens drop it.
func parseSpan(s string, allowZero bool) (span, bool) {
	if strings.Contains(s, "-") {
		return parseRange(s)
	}
	if strings.Contains(s, "/") {
		return parseCIDR(s, allowZero)
	}
	a, ok := parseAddr(s)
	if !ok {
		return span{}, false
	}
	u := FromAddr(a)
	sp := span{lo: u, hi: u}
	if a.Is4() {
		sp.flags = FlagV4
	}
	return sp, true
}

func parseRange(s string) (span, bool) {
	l, r, _ := strings.Cut(s, "-")
	l, r = strings.TrimSpace(l), strings.TrimSpace(r)
	if i := strings.IndexByte(r, '/'); i >= 0 { // Whereabouts: a-b/n
		r = strings.TrimSpace(r[:i])
	}
	a, ok1 := parseAddr(l)
	z, ok2 := parseAddr(r)
	if !ok1 || !ok2 || a.Is4() != z.Is4() {
		return span{}, false
	}
	lu, hu := FromAddr(a), FromAddr(z)
	if lu.Cmp(hu) > 0 {
		return span{}, false
	}
	sp := span{lo: lu, hi: hu, flags: FlagRange}
	if a.Is4() {
		sp.flags |= FlagV4
	}
	// Equal sides are one address, unless its text ends in ':' (it would read back as a prefix).
	if lu == hu && !strings.HasSuffix(a.String(), ":") {
		sp.flags &^= FlagRange
	}
	return sp, true
}

func parseCIDR(s string, allowZero bool) (span, bool) {
	if strings.HasSuffix(s, "/") { // "10.0.0.0/": still typing, the address
		return parseSpan(s[:len(s)-1], allowZero)
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return span{}, false
	}
	p = p.Masked()
	a, nb := p.Addr().WithZone(""), p.Bits()
	if a.Is4In6() && nb >= 96 {
		a, nb = a.Unmap(), nb-96
	}
	if nb == 0 && !allowZero {
		return span{}, false
	}
	u := FromAddr(a)
	size := 128
	sp := span{lo: u, flags: FlagCIDR, bits: uint8(nb)}
	if a.Is4() {
		sp.flags |= FlagV4
		size = 32
	}
	if nb == size { // full length: a single address
		sp.hi, sp.flags, sp.bits = u, sp.flags&FlagV4, 0
		return sp, true
	}
	sp.hi = u.or(hostMask(size - nb))
	return sp, true
}
