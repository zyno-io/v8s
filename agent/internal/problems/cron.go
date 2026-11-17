package problems

import (
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed 5-field cron expression (minute hour day-of-month month day-of-week).
type cronSpec struct {
	min, hour, dom, month, dow uint64
	domStar, dowStar           bool
}

func parseCronField(f string, lo, hi int) (uint64, bool, bool) {
	var bits uint64
	star := false
	for _, part := range strings.Split(f, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return 0, false, false
			}
			step, part = n, base
		}
		a, b := lo, hi
		switch {
		case part == "*" || part == "?":
			star = star || step == 1
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			ai, e1 := strconv.Atoi(x)
			bi, e2 := strconv.Atoi(y)
			if e1 != nil || e2 != nil {
				return 0, false, false
			}
			a, b = ai, bi
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return 0, false, false
			}
			a = n
			b = n
			if step != 1 {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, false
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, star, true
}

func parseCron(s string) (cronSpec, bool) {
	s = strings.TrimSpace(s)
	switch s {
	case "@yearly", "@annually":
		s = "0 0 1 1 *"
	case "@monthly":
		s = "0 0 1 * *"
	case "@weekly":
		s = "0 0 * * 0"
	case "@daily", "@midnight":
		s = "0 0 * * *"
	case "@hourly":
		s = "0 * * * *"
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return cronSpec{}, false
	}
	var c cronSpec
	var ok [5]bool
	c.min, _, ok[0] = parseCronField(f[0], 0, 59)
	c.hour, _, ok[1] = parseCronField(f[1], 0, 23)
	c.dom, c.domStar, ok[2] = parseCronField(f[2], 1, 31)
	c.month, _, ok[3] = parseCronField(f[3], 1, 12)
	c.dow, c.dowStar, ok[4] = parseCronField(f[4], 0, 7)
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	for _, o := range ok {
		if !o {
			return cronSpec{}, false
		}
	}
	return c, true
}

func (c cronSpec) dayOK(t time.Time) bool {
	d := c.dom&(1<<uint(t.Day())) != 0
	w := c.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return w
	case c.dowStar:
		return d
	}
	return d || w
}

// next returns the first fire time strictly after t (UTC), or the zero time when none exists within 5 years.
func (c cronSpec) next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayOK(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case c.min&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// cronInterval estimates the gap between two consecutive runs of a schedule (the larger of the next two
// gaps, so "0 9 * * 1-5" counts the weekend). ok is false for an unparsable schedule.
func cronInterval(schedule string, from time.Time) (time.Duration, bool) {
	c, ok := parseCron(schedule)
	if !ok {
		return 0, false
	}
	a := c.next(from)
	if a.IsZero() {
		return 0, false
	}
	b := c.next(a)
	if b.IsZero() {
		return 0, false
	}
	d := b.Sub(a)
	if e := c.next(b); !e.IsZero() && e.Sub(b) > d {
		d = e.Sub(b)
	}
	return d, true
}
