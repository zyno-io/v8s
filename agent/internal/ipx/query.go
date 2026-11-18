package ipx

import (
	"net/netip"
	"strings"
)

// Mode is what kind of IP query the text is.
type Mode uint8

const (
	ModeAddr   Mode = iota // one address
	ModeCIDR               // a CIDR block
	ModeRange              // an inclusive a-b range
	ModePrefix             // a partial address being typed ("10.42.", "fd00:10:")
)

func (m Mode) String() string {
	switch m {
	case ModeAddr:
		return "addr"
	case ModeCIDR:
		return "cidr"
	case ModeRange:
		return "range"
	case ModePrefix:
		return "prefix"
	}
	return ""
}

// Match is how a token relates to a query; the zero value is no match.
type Match uint8

const (
	MatchNone     Match = iota
	MatchExact          // the same address, or the same span
	MatchContains       // the token's span contains the query
	MatchWithin         // the token lies inside the query's CIDR or range
	MatchOverlap        // the spans partly overlap
	MatchPrefix         // the token starts with the typed prefix
)

func (m Match) String() string {
	switch m {
	case MatchExact:
		return "exact"
	case MatchContains:
		return "contains"
	case MatchWithin:
		return "within"
	case MatchOverlap:
		return "overlap"
	case MatchPrefix:
		return "prefix"
	}
	return ""
}

type qspan struct{ Lo, Hi U128 }

// Query is a parsed IP query.
type Query struct {
	Mode   Mode
	V4     bool
	Text   string // normalized: "10.0.0.5", "10.0.0.0/16", "a-b", or the trimmed prefix
	Bits   uint8  // CIDR prefix length (the family's own)
	n      uint8
	spans  [4]qspan
	strong bool
}

// Strong reports whether the query is specific enough to start on-demand watches: an address, CIDR or range,
// or a prefix with at least two complete IPv4 octets or IPv6 groups.
func (q *Query) Strong() bool { return q.strong }

const maxQueryLen = 100

// Parse reads s as an IP query. See docs/guide/navigation.md for the accepted forms. Text that merely looks
// like one ("v1.2") parses: a wrong detection only adds an empty IP section.
func Parse(s string) (*Query, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxQueryLen {
		return nil, false
	}
	// 1. brackets: "[v6]", "[v6]:port"; a lone "[" is a user still typing
	if s[0] == '[' {
		if i := strings.IndexByte(s, ']'); i > 0 {
			s = s[1:i]
		} else {
			s = s[1:]
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, false
		}
	}
	// 2. zone
	s = stripZone(s)
	// 3. range
	if strings.Contains(s, "-") {
		sp, ok := parseRange(s)
		if !ok {
			return nil, false
		}
		m := ModeRange
		if sp.flags&FlagRange == 0 {
			m = ModeAddr
		}
		return spanQuery(sp, m), true
	}
	// 4. CIDR
	if strings.Contains(s, "/") {
		if !strings.HasSuffix(s, "/") {
			sp, ok := parseCIDR(s, true)
			if !ok {
				return nil, false
			}
			m := ModeCIDR
			if sp.flags&FlagCIDR == 0 {
				m = ModeAddr
			}
			return spanQuery(sp, m), true
		}
		s = strings.TrimSuffix(s, "/") // "10.0.0.0/": still typing, the address
		if s == "" || strings.Contains(s, "/") {
			return nil, false
		}
	}
	// 5. v4 with port
	if i := strings.IndexByte(s, ':'); i > 0 && strings.Count(s, ":") == 1 {
		if port := s[i+1:]; len(port) >= 1 && len(port) <= 5 && allDigits(port) {
			if a, err := netip.ParseAddr(s[:i]); err == nil && a.Is4() {
				s = s[:i]
			}
		}
	}
	// 6. full address (a v6 address ending in ':' is a prefix being typed)
	if a, err := netip.ParseAddr(s); err == nil && !(a.Is6() && strings.HasSuffix(s, ":")) {
		return spanQuery(span{lo: FromAddr(a), hi: FromAddr(a), flags: v4Flag(a.Unmap())}, ModeAddr), true
	}
	// 7. partial v4, 8. partial v6
	if q, ok := parseV4Prefix(s); ok {
		return q, true
	}
	return parseV6Prefix(s)
}

func v4Flag(a netip.Addr) Flags {
	if a.Is4() {
		return FlagV4
	}
	return 0
}

func spanQuery(sp span, m Mode) *Query {
	q := &Query{Mode: m, V4: sp.flags&FlagV4 != 0, Bits: sp.bits, n: 1, strong: true}
	q.spans[0] = qspan{sp.lo, sp.hi}
	q.Text = Tok{Lo: sp.lo, Hi: sp.hi, Flags: sp.flags, Bits: sp.bits}.String()
	if m == ModeAddr && strings.HasSuffix(q.Text, ":") { // "fd00::" would read back as a prefix
		q.Text = sp.lo.Addr(false).StringExpanded()
	}
	return q
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func hexVal(s string) uint64 {
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a':
			c = c - 'a' + 10
		case c >= 'A':
			c = c - 'A' + 10
		default:
			c -= '0'
		}
		v = v<<4 | uint64(c)
	}
	return v
}

// parseV4Prefix reads "10.", "10.42", "10.42.3." and the like.
func parseV4Prefix(s string) (*Query, bool) {
	parts := strings.Split(s, ".")
	d := len(parts) - 1
	if d < 1 || d > 3 {
		return nil, false
	}
	var base uint32
	for i := 0; i < d; i++ {
		p := parts[i]
		if len(p) == 0 || len(p) > 3 || !allDigits(p) || (len(p) > 1 && p[0] == '0') {
			return nil, false
		}
		v := uint32(0)
		for j := 0; j < len(p); j++ {
			v = v*10 + uint32(p[j]-'0')
		}
		if v > 255 {
			return nil, false
		}
		base |= v << (8 * (3 - i))
	}
	t := parts[d]
	if len(t) > 3 || !allDigits(t) || (len(t) > 1 && t[0] == '0') || (d == 3 && t != "") {
		return nil, false
	}
	q := &Query{Mode: ModePrefix, V4: true, Text: s, strong: d >= 2}
	shift := uint(8 * (3 - d))
	add := func(a, b uint32) {
		if a > 255 {
			return
		}
		b = min(b, 255)
		q.spans[q.n] = qspan{fromV4(base | a<<shift), fromV4(base | b<<shift | (uint32(1)<<shift - 1))}
		q.n++
	}
	if t == "" {
		add(0, 255)
	} else {
		v := uint32(0)
		for j := 0; j < len(t); j++ {
			v = v*10 + uint32(t[j]-'0')
		}
		add(v, v)
		if t != "0" {
			if len(t) < 3 {
				add(v*10, v*10+9)
			}
			if len(t) < 2 {
				add(v*100, v*100+99)
			}
		}
	}
	if q.n == 0 {
		return nil, false
	}
	return q, true
}

// putGroup places a 16-bit group value at index i (0..7) of a 128-bit address.
func putGroup(u U128, i int, v uint64) U128 {
	if i < 4 {
		u.Hi |= v << (16 * (3 - i))
	} else {
		u.Lo |= v << (16 * (7 - i))
	}
	return u
}

// parseV6Prefix reads "fd00::", "fd00:10:", "fd00:10:a".
func parseV6Prefix(s string) (*Query, bool) {
	colons := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == ':':
			colons++
		case !isHex(s[i]):
			return nil, false
		}
	}
	if colons < 2 || strings.Contains(s, ":::") {
		return nil, false
	}
	var head, t string
	if strings.HasSuffix(s, "::") {
		head = s[:len(s)-2]
	} else {
		if strings.Contains(s, "::") {
			return nil, false
		}
		i := strings.LastIndexByte(s, ':')
		head, t = s[:i], s[i+1:]
	}
	if head == "" || strings.Contains(head, "::") {
		return nil, false
	}
	groups := strings.Split(head, ":")
	if len(groups) > 7 || len(t) > 4 {
		return nil, false
	}
	var base U128
	for i, g := range groups {
		if len(g) == 0 || len(g) > 4 {
			return nil, false
		}
		base = putGroup(base, i, hexVal(g))
	}
	gi := len(groups)
	q := &Query{Mode: ModePrefix, Text: s, strong: len(groups) >= 2}
	shift := uint(16 * (7 - gi))
	add := func(a, b uint64) {
		q.spans[q.n] = qspan{putGroup(base, gi, a), putGroup(base, gi, b).or(hostMask(int(shift)))}
		q.n++
	}
	switch {
	case t == "":
		add(0, 0xffff)
	case t[0] == '0':
		v := hexVal(t)
		add(v, v)
	default:
		v := hexVal(t)
		add(v, v)
		if len(t) < 4 {
			add(v<<4, v<<4|0xf)
		}
		if len(t) < 3 {
			add(v<<8, v<<8|0xff)
		}
		if len(t) < 2 {
			add(v<<12, v<<12|0xfff)
		}
	}
	return q, true
}

// ---- matching ---------------------------------------------------------------------------

// Match relates one token to the query (MatchNone: unrelated, or the other address family).
func (q *Query) Match(t *Tok) Match {
	if t.V4() != q.V4 {
		return MatchNone
	}
	switch q.Mode {
	case ModeAddr:
		a := q.spans[0].Lo
		if t.Lo == a && t.Hi == a && t.Single() {
			return MatchExact
		}
		if t.Lo.le(a) && a.le(t.Hi) {
			return MatchContains
		}
	case ModeCIDR, ModeRange:
		l, h := q.spans[0].Lo, q.spans[0].Hi
		switch {
		case t.Lo == l && t.Hi == h:
			return MatchExact
		case l.le(t.Lo) && t.Hi.le(h):
			return MatchWithin
		case t.Lo.le(l) && h.le(t.Hi):
			return MatchContains
		case t.Lo.le(h) && l.le(t.Hi):
			return MatchOverlap
		}
	case ModePrefix:
		single := t.Single()
		m := MatchNone
		for i := uint8(0); i < q.n; i++ {
			s := &q.spans[i]
			switch {
			case !single && t.Lo.le(s.Lo) && s.Hi.le(t.Hi):
				return MatchContains
			case t.Lo.le(s.Hi) && s.Lo.le(t.Hi):
				m = MatchPrefix
			}
		}
		return m
	}
	return MatchNone
}

// RankOf ranks a match: lower is better. tier<<16 | secondary<<8 | sizeBits, where sizeBits (contains only)
// prefers the most specific span.
func RankOf(m Match, t *Tok) uint32 {
	var tier uint32
	switch m {
	case MatchExact:
		tier = 0
	case MatchContains:
		tier = 1
	case MatchWithin, MatchOverlap:
		tier = 2
	default:
		tier = 3
	}
	r := tier << 16
	if t.Field.Secondary() {
		r |= 1 << 8
	}
	if m == MatchContains {
		r |= uint32(t.Hi.sub(t.Lo).bitLen())
	}
	return r
}

// Best returns the best-ranked matching token of ts and its index (MatchNone, -1 when none match).
func (q *Query) Best(ts []Tok) (Match, int) {
	best, bi := MatchNone, -1
	var br uint32
	for i := range ts {
		m := q.Match(&ts[i])
		if m == MatchNone {
			continue
		}
		if bi < 0 {
			best, bi, br = m, i, RankOf(m, &ts[i])
			continue
		}
		if r := RankOf(m, &ts[i]); r < br {
			best, bi, br = m, i, r
		}
	}
	return best, bi
}

// Any reports whether any token matches.
func (q *Query) Any(ts []Tok) bool {
	for i := range ts {
		if q.Match(&ts[i]) != MatchNone {
			return true
		}
	}
	return false
}
