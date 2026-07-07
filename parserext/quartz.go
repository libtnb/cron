// Package parserext provides optional cron parser extensions.
package parserext

import (
	"errors"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/internal/bitmask"
)

// nextYearLimit caps QuartzSchedule.Next search; matches SpecSchedule.
const nextYearLimit = 5

var (
	monthNames = map[string]uint{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dowNames = map[string]uint{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

type quartzParser struct {
	loc  *time.Location
	std5 *cron.StandardParser
	std6 *cron.StandardParser
}

// NewQuartzParser handles standard specs plus the Quartz tokens L, L-n, LW,
// nW, N#M, and NL. Numeric day-of-week stays cron-style 0-6 Sunday-first (not
// Quartz's 1-7); name forms (FRI#3, FRIL) are unambiguous. Specs without
// Quartz-only tokens are forwarded to the standard parser.
func NewQuartzParser(loc *time.Location) cron.Parser {
	if loc == nil {
		loc = time.Local
	}
	return &quartzParser{
		loc:  loc,
		std5: cron.NewStandardParser(cron.WithDefaultLocation(loc)),
		std6: cron.NewStandardParser(cron.WithDefaultLocation(loc), cron.WithSeconds()),
	}
}

func (p *quartzParser) Parse(spec string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, &cron.ParseError{Spec: spec, Pos: -1, Reason: "empty spec"}
	}
	if trimmed[0] == '@' {
		return p.std5.Parse(spec)
	}

	loc := p.loc
	if i := strings.IndexByte(trimmed, ' '); i > 0 {
		head := trimmed[:i]
		if eq := strings.IndexByte(head, '='); eq > 0 {
			key := head[:eq]
			if key == "TZ" || key == "CRON_TZ" {
				zone := head[eq+1:]
				l, err := time.LoadLocation(zone)
				if err != nil {
					return nil, &cron.ParseError{Spec: spec, Field: key, Pos: 0,
						Reason: "unknown time zone " + strconv.Quote(zone), Err: err}
				}
				loc = l
				trimmed = strings.TrimSpace(trimmed[i+1:])
				if trimmed != "" && trimmed[0] == '@' {
					return p.std5.Parse(spec)
				}
			}
		}
	}

	fields := strings.Fields(trimmed)
	if !hasQuartzTokens(fields) {
		switch len(fields) {
		case 5:
			return p.std5.Parse(spec)
		case 6:
			return p.std6.Parse(spec)
		default:
			return nil, &cron.ParseError{Spec: spec, Pos: -1,
				Reason: "expected 5 or 6 fields, got " + strconv.Itoa(len(fields))}
		}
	}

	switch len(fields) {
	case 5:
		return parseQuartz5(spec, fields, loc)
	case 6:
		return parseQuartz6(spec, fields, loc)
	default:
		return nil, &cron.ParseError{Spec: spec, Pos: -1,
			Reason: "expected 5 or 6 fields with quartz tokens, got " + strconv.Itoa(len(fields))}
	}
}

// QuartzSchedule supports the Quartz subset parsed by NewQuartzParser.
type QuartzSchedule struct {
	second uint64 // bitmap 0-59
	minute uint64 // bitmap 0-59
	hour   uint64 // bitmap 0-23
	month  uint64 // bitmap 1-12
	dom    domRule
	dow    dowRule
	loc    *time.Location
}

type domRule struct {
	bitmap         uint64 // standard bitmap (bits 1..31)
	star           bool   // originally "*" or "?"
	last           bool   // "L" / "L-n": last day of the month, minus offset
	lastOffset     int    // days before the last day for "L-n"
	lastWeekday    bool   // "LW": last weekday of the month
	nearestWeekday int    // "nW": weekday nearest to day n; 0 if unset
}

type dowRule struct {
	bitmap     uint64 // standard bitmap (bits 0..6)
	star       bool   // originally "*" or "?"
	nthWeekday int    // 0..6 weekday for N#M, -1 if unset
	nthN       int    // 1..5 occurrence within month, 0 if unset
	lastN      int    // 0..6 weekday for NL, -1 if unset
}

// Location returns the schedule's evaluation time zone.
func (s *QuartzSchedule) Location() *time.Location {
	return s.loc
}

// Next returns the next firing after t, or zero if none is found. It shares
// SpecSchedule.Next's structure: bitmask jumps with wall-clock reconstruction
// for the hour so DST spring-forward days are handled correctly.
func (s *QuartzSchedule) Next(t time.Time) time.Time {
	origLoc := t.Location()
	loc := s.loc
	if loc == nil { // zero-value QuartzSchedule
		loc = time.Local
	}
	if loc != origLoc {
		t = t.In(loc)
	}

	t = t.Add(time.Second - time.Duration(t.Nanosecond())*time.Nanosecond)
	yearLimit := t.Year() + nextYearLimit

	for {
		year, month, day := t.Date()
		if year > yearLimit {
			return time.Time{}
		}

		if 1<<uint(month)&s.month == 0 {
			m := bitmask.NextInRange(s.month, uint(month)+1, 12)
			if m < 0 {
				m = bitmask.NextInRange(s.month, 1, 12)
				if m < 0 {
					return time.Time{} // zero-value QuartzSchedule: empty month set
				}
				t = time.Date(year+1, time.Month(m), 1, 0, 0, 0, 0, loc)
			} else {
				t = time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc)
			}
			continue
		}

		if !s.dayMatches(t) {
			t = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
			continue
		}

		hour, minute, sec := t.Clock()

		if 1<<uint(hour)&s.hour == 0 {
			h := bitmask.NextInRange(s.hour, uint(hour)+1, 23)
			if h < 0 {
				t = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
				continue
			}
			// See SpecSchedule.Next: time.Date resolves a nonexistent
			// spring-forward hour to the pre-gap wall time, which wouldn't
			// advance; step one wall hour instead and re-validate.
			cand := time.Date(year, month, day, h, 0, 0, 0, loc)
			if cand.Hour() == h && cand.After(t) {
				t = cand
			} else {
				t = t.Add(time.Hour -
					time.Duration(minute)*time.Minute -
					time.Duration(sec)*time.Second)
			}
			continue
		}

		if 1<<uint(minute)&s.minute == 0 {
			m := bitmask.NextInRange(s.minute, uint(minute)+1, 59)
			if m < 0 {
				t = t.Add(time.Hour -
					time.Duration(minute)*time.Minute -
					time.Duration(sec)*time.Second)
				continue
			}
			t = t.Add(time.Duration(m-minute)*time.Minute -
				time.Duration(sec)*time.Second)
			continue
		}

		if 1<<uint(sec)&s.second == 0 {
			n := bitmask.NextInRange(s.second, uint(sec)+1, 59)
			if n < 0 {
				t = t.Add(time.Minute - time.Duration(sec)*time.Second)
				continue
			}
			t = t.Add(time.Duration(n-sec) * time.Second)
			continue
		}

		return t.In(origLoc)
	}
}

// Upcoming is a lazy iterator over future firings.
func (s *QuartzSchedule) Upcoming(from time.Time) iter.Seq[time.Time] {
	return func(yield func(time.Time) bool) {
		cur := from
		for {
			next := s.Next(cur)
			if next.IsZero() || !yield(next) {
				return
			}
			cur = next
		}
	}
}

func (s *QuartzSchedule) dayMatches(t time.Time) bool {
	domOK := s.checkDom(t)
	dowOK := s.checkDow(t)
	if s.dom.star || s.dow.star {
		return domOK && dowOK
	}
	return domOK || dowOK
}

func (s *QuartzSchedule) checkDom(t time.Time) bool {
	switch {
	case s.dom.last:
		return t.Day() == lastDayOfMonth(t.Year(), t.Month(), s.loc)-s.dom.lastOffset
	case s.dom.lastWeekday:
		return t.Day() == lastWeekdayOfMonth(t.Year(), t.Month(), s.loc)
	case s.dom.nearestWeekday > 0:
		return t.Day() == nearestWeekdayTo(t.Year(), t.Month(), s.dom.nearestWeekday, s.loc)
	default:
		return 1<<uint(t.Day())&s.dom.bitmap != 0
	}
}

func (s *QuartzSchedule) checkDow(t time.Time) bool {
	wd := int(t.Weekday())
	if s.dow.nthWeekday >= 0 {
		if wd != s.dow.nthWeekday {
			return false
		}
		n := (t.Day()-1)/7 + 1
		return n == s.dow.nthN
	}
	if s.dow.lastN >= 0 {
		if wd != s.dow.lastN {
			return false
		}
		return t.Day()+7 > lastDayOfMonth(t.Year(), t.Month(), s.loc)
	}
	return 1<<uint(wd)&s.dow.bitmap != 0
}

func hasQuartzTokens(fields []string) bool {
	return slices.ContainsFunc(fields, tokenIsQuartz)
}

func tokenIsQuartz(s string) bool {
	if s == "L" || s == "LW" || strings.HasPrefix(s, "L-") {
		return true
	}
	if strings.Contains(s, "#") {
		return true
	}
	if len(s) < 2 {
		return false
	}
	head := s[:len(s)-1]
	switch s[len(s)-1] {
	case 'L':
		if _, err := strconv.Atoi(head); err == nil {
			return true
		}
		_, ok := dowNames[strings.ToLower(head)]
		return ok
	case 'W':
		_, err := strconv.Atoi(head)
		return err == nil
	}
	return false
}

func parseQuartz5(spec string, fields []string, loc *time.Location) (cron.Schedule, error) {
	return buildQuartz(spec, 1<<0, fields[0], fields[1], fields[2], fields[3], fields[4], loc)
}

func parseQuartz6(spec string, fields []string, loc *time.Location) (cron.Schedule, error) {
	sec, err := bitmap(fields[0], 0, 59, nil)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "second", Reason: err.Error(), Pos: -1}
	}
	return buildQuartz(spec, sec, fields[1], fields[2], fields[3], fields[4], fields[5], loc)
}

func buildQuartz(spec string, sec uint64, minute, hour, dom, month, dow string, loc *time.Location) (cron.Schedule, error) {
	min, err := bitmap(minute, 0, 59, nil)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "minute", Reason: err.Error(), Pos: -1}
	}
	hr, err := bitmap(hour, 0, 23, nil)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "hour", Reason: err.Error(), Pos: -1}
	}
	mo, err := bitmap(month, 1, 12, monthNames)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "month", Reason: err.Error(), Pos: -1}
	}
	domR, err := parseDom(dom)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "dom", Reason: err.Error(), Pos: -1}
	}
	dowR, err := parseDow(dow)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "dow", Reason: err.Error(), Pos: -1}
	}
	return &QuartzSchedule{
		second: sec,
		minute: min,
		hour:   hr,
		month:  mo,
		dom:    domR,
		dow:    dowR,
		loc:    loc,
	}, nil
}

func parseDom(field string) (domRule, error) {
	switch {
	case field == "L":
		return domRule{last: true}, nil
	case field == "LW":
		return domRule{lastWeekday: true}, nil
	case strings.HasPrefix(field, "L-"):
		n, err := strconv.Atoi(field[2:])
		if err != nil || n < 1 || n > 30 {
			return domRule{}, errors.New("invalid L-offset token " + strconv.Quote(field))
		}
		return domRule{last: true, lastOffset: n}, nil
	case len(field) >= 2 && field[len(field)-1] == 'W':
		n, err := strconv.Atoi(field[:len(field)-1])
		if err != nil || n < 1 || n > 31 {
			return domRule{}, errors.New("invalid W token " + strconv.Quote(field))
		}
		return domRule{nearestWeekday: n}, nil
	case field == "*" || field == "?":
		bm, _ := bitmap(field, 1, 31, nil)
		return domRule{bitmap: bm, star: true}, nil
	}
	bm, err := bitmap(field, 1, 31, nil)
	if err != nil {
		return domRule{}, err
	}
	return domRule{bitmap: bm}, nil
}

func parseDow(field string) (dowRule, error) {
	if field == "*" || field == "?" {
		bm, _ := bitmap(field, 0, 6, nil)
		return dowRule{bitmap: bm, star: true, nthWeekday: -1, lastN: -1}, nil
	}
	if i := strings.IndexByte(field, '#'); i > 0 {
		w, okW := parseDowValue(field[:i])
		n, err := strconv.Atoi(field[i+1:])
		if !okW || err != nil || n < 1 || n > 5 {
			return dowRule{}, errors.New("invalid N#M token: " + strconv.Quote(field))
		}
		return dowRule{nthWeekday: w, nthN: n, lastN: -1}, nil
	}
	if field == "L" { // Quartz: bare L in dow means Saturday
		return dowRule{bitmap: 1 << 6, nthWeekday: -1, lastN: -1}, nil
	}
	if len(field) >= 2 && field[len(field)-1] == 'L' {
		w, ok := parseDowValue(field[:len(field)-1])
		if !ok {
			return dowRule{}, errors.New("invalid NL token: " + strconv.Quote(field))
		}
		return dowRule{lastN: w, nthWeekday: -1}, nil
	}
	bm, err := bitmap(field, 0, 6, dowNames)
	if err != nil {
		return dowRule{}, err
	}
	return dowRule{bitmap: bm, nthWeekday: -1, lastN: -1}, nil
}

// parseDowValue resolves a 0-6 weekday number or a sun..sat name.
func parseDowValue(s string) (int, bool) {
	if v, err := strconv.Atoi(s); err == nil {
		if v < 0 || v > 6 {
			return 0, false
		}
		return v, true
	}
	if v, ok := dowNames[strings.ToLower(s)]; ok {
		return int(v), true
	}
	return 0, false
}

func lastDayOfMonth(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}

func lastWeekdayOfMonth(year int, month time.Month, loc *time.Location) int {
	d := lastDayOfMonth(year, month, loc)
	switch time.Date(year, month, d, 0, 0, 0, 0, loc).Weekday() {
	case time.Saturday:
		return d - 1
	case time.Sunday:
		return d - 2
	default:
		return d
	}
}

// nearestWeekdayTo resolves Quartz nW: the weekday closest to day n without
// crossing into another month, or -1 when the month has no day n.
func nearestWeekdayTo(year int, month time.Month, day int, loc *time.Location) int {
	last := lastDayOfMonth(year, month, loc)
	if day > last {
		return -1
	}
	switch time.Date(year, month, day, 0, 0, 0, 0, loc).Weekday() {
	case time.Saturday:
		if day > 1 {
			return day - 1
		}
		return day + 2
	case time.Sunday:
		if day < last {
			return day + 1
		}
		return day - 2
	default:
		return day
	}
}

func bitmap(field string, lo, hi uint, names map[string]uint) (uint64, error) {
	if field == "*" || field == "?" {
		var bm uint64
		for v := lo; v <= hi; v++ {
			bm |= 1 << v
		}
		return bm, nil
	}
	var out uint64
	for part := range strings.SplitSeq(field, ",") {
		bm, err := parsePart(part, lo, hi, names)
		if err != nil {
			return 0, err
		}
		out |= bm
	}
	return out, nil
}

func parsePart(part string, lo, hi uint, names map[string]uint) (uint64, error) {
	step := uint(1)
	if head, tail, hasStep := strings.Cut(part, "/"); hasStep {
		s, err := strconv.ParseUint(tail, 10, 32)
		if err != nil || s == 0 {
			return 0, errors.New("invalid step " + strconv.Quote(part))
		}
		step = uint(s)
		part = head
	}
	var start, end uint
	switch part {
	case "*", "?":
		start, end = lo, hi
	default:
		if lhs, rhs, hasRange := strings.Cut(part, "-"); hasRange {
			a, okA := parseValue(lhs, names)
			b, okB := parseValue(rhs, names)
			if !okA || !okB {
				return 0, errors.New("invalid range " + strconv.Quote(part))
			}
			start, end = a, b
		} else {
			v, ok := parseValue(part, names)
			if !ok {
				return 0, errors.New("invalid number " + strconv.Quote(part))
			}
			start, end = v, v
		}
	}
	if start < lo || end > hi {
		return 0, errors.New("value out of range " + strconv.Quote(part))
	}
	if start > end {
		return 0, errors.New("range start > end in " + strconv.Quote(part))
	}
	var bm uint64
	// Iterate in uint64 so a huge step cannot wrap on 32-bit platforms.
	for v := uint64(start); v <= uint64(end); v += uint64(step) {
		bm |= 1 << v
	}
	return bm, nil
}

func parseValue(s string, names map[string]uint) (uint, bool) {
	if v, err := strconv.ParseUint(s, 10, 32); err == nil {
		return uint(v), true
	}
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, true
		}
	}
	return 0, false
}
