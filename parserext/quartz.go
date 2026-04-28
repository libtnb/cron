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
)

// NewQuartzParser handles standard specs plus Quartz L, N#M, and NL tokens.
// Specs without Quartz-only tokens are forwarded to the standard parser.
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

type quartzParser struct {
	loc  *time.Location
	std5 *cron.StandardParser
	std6 *cron.StandardParser
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

func hasQuartzTokens(fields []string) bool {
	return slices.ContainsFunc(fields, tokenIsQuartz)
}

func tokenIsQuartz(s string) bool {
	if s == "L" {
		return true
	}
	if strings.Contains(s, "#") {
		return true
	}
	if len(s) >= 2 && s[len(s)-1] == 'L' {
		if _, err := strconv.Atoi(s[:len(s)-1]); err == nil {
			return true
		}
	}
	if len(s) >= 2 && s[len(s)-1] == 'W' {
		if _, err := strconv.Atoi(s[:len(s)-1]); err == nil {
			return true
		}
	}
	return false
}

func parseQuartz5(spec string, fields []string, loc *time.Location) (cron.Schedule, error) {
	return buildQuartz(spec, 1<<0, fields[0], fields[1], fields[2], fields[3], fields[4], loc)
}

func parseQuartz6(spec string, fields []string, loc *time.Location) (cron.Schedule, error) {
	sec, err := bitmap(fields[0], 0, 59)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "second", Reason: err.Error(), Pos: -1}
	}
	return buildQuartz(spec, sec, fields[1], fields[2], fields[3], fields[4], fields[5], loc)
}

func buildQuartz(spec string, sec uint64, minute, hour, dom, month, dow string, loc *time.Location) (cron.Schedule, error) {
	min, err := bitmap(minute, 0, 59)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "minute", Reason: err.Error(), Pos: -1}
	}
	hr, err := bitmap(hour, 0, 23)
	if err != nil {
		return nil, &cron.ParseError{Spec: spec, Field: "hour", Reason: err.Error(), Pos: -1}
	}
	mo, err := bitmap(month, 1, 12)
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

// Location returns the schedule's evaluation time zone.
func (s *QuartzSchedule) Location() *time.Location {
	return s.loc
}

type domRule struct {
	bitmap uint64 // standard bitmap (bits 1..31)
	star   bool   // originally "*" or "?"
	last   bool   // "L" - last day of the month
}

type dowRule struct {
	bitmap     uint64 // standard bitmap (bits 0..6)
	star       bool   // originally "*" or "?"
	nthWeekday int    // 0..6 weekday for N#M, -1 if unset
	nthN       int    // 1..5 occurrence within month, 0 if unset
	lastN      int    // 0..6 weekday for NL, -1 if unset
}

func parseDom(field string) (domRule, error) {
	if field == "L" {
		return domRule{last: true}, nil
	}
	if strings.ContainsRune(field, 'W') {
		return domRule{}, errors.New("'W' nearest-weekday is not supported")
	}
	if field == "*" || field == "?" {
		bm, _ := bitmap(field, 1, 31)
		return domRule{bitmap: bm, star: true}, nil
	}
	bm, err := bitmap(field, 1, 31)
	if err != nil {
		return domRule{}, err
	}
	return domRule{bitmap: bm}, nil
}

func parseDow(field string) (dowRule, error) {
	if field == "*" || field == "?" {
		bm, _ := bitmap(field, 0, 6)
		return dowRule{bitmap: bm, star: true, nthWeekday: -1, lastN: -1}, nil
	}
	if i := strings.IndexByte(field, '#'); i > 0 {
		w, errA := strconv.Atoi(field[:i])
		n, errB := strconv.Atoi(field[i+1:])
		if errA != nil || errB != nil || w < 0 || w > 6 || n < 1 || n > 5 {
			return dowRule{}, errors.New("invalid N#M token: " + strconv.Quote(field))
		}
		return dowRule{nthWeekday: w, nthN: n, lastN: -1}, nil
	}
	if len(field) >= 2 && field[len(field)-1] == 'L' {
		w, err := strconv.Atoi(field[:len(field)-1])
		if err != nil || w < 0 || w > 6 {
			return dowRule{}, errors.New("invalid NL token: " + strconv.Quote(field))
		}
		return dowRule{lastN: w, nthWeekday: -1}, nil
	}
	if field == "L" {
		return dowRule{bitmap: 1 << 6, nthWeekday: -1, lastN: -1}, nil
	}
	bm, err := bitmap(field, 0, 6)
	if err != nil {
		return dowRule{}, err
	}
	return dowRule{bitmap: bm, nthWeekday: -1, lastN: -1}, nil
}

func (s *QuartzSchedule) Next(t time.Time) time.Time {
	loc := s.loc
	orig := t.Location()
	t = t.In(loc).Add(time.Second - time.Duration(t.Nanosecond())*time.Nanosecond)
	yearLimit := t.Year() + 5
	for t.Year() <= yearLimit {
		if 1<<uint(t.Month())&s.month == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if 1<<uint(t.Hour())&s.hour == 0 {
			t = t.Add(time.Hour -
				time.Duration(t.Minute())*time.Minute -
				time.Duration(t.Second())*time.Second)
			continue
		}
		if 1<<uint(t.Minute())&s.minute == 0 {
			t = t.Add(time.Minute - time.Duration(t.Second())*time.Second)
			continue
		}
		if 1<<uint(t.Second())&s.second == 0 {
			t = t.Add(time.Second)
			continue
		}
		return t.In(orig)
	}
	return time.Time{}
}

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
	if s.dom.last {
		return t.Day() == lastDayOfMonth(t.Year(), t.Month(), s.loc)
	}
	return 1<<uint(t.Day())&s.dom.bitmap != 0
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

func lastDayOfMonth(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}

func bitmap(field string, lo, hi uint) (uint64, error) {
	if field == "*" || field == "?" {
		var bm uint64
		for v := lo; v <= hi; v++ {
			bm |= 1 << v
		}
		return bm, nil
	}
	var out uint64
	for part := range strings.SplitSeq(field, ",") {
		bm, err := parsePart(part, lo, hi)
		if err != nil {
			return 0, err
		}
		out |= bm
	}
	return out, nil
}

func parsePart(part string, lo, hi uint) (uint64, error) {
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
		if lhsStr, rhsStr, hasRange := strings.Cut(part, "-"); hasRange {
			lhs, errA := strconv.ParseUint(lhsStr, 10, 32)
			rhs, errB := strconv.ParseUint(rhsStr, 10, 32)
			if errA != nil || errB != nil {
				return 0, errors.New("invalid range " + strconv.Quote(part))
			}
			start, end = uint(lhs), uint(rhs)
		} else {
			v, err := strconv.ParseUint(part, 10, 32)
			if err != nil {
				return 0, errors.New("invalid number " + strconv.Quote(part))
			}
			start = uint(v)
			end = start
		}
	}
	if start < lo || end > hi {
		return 0, errors.New("value out of range " + strconv.Quote(part))
	}
	if start > end {
		return 0, errors.New("range start > end in " + strconv.Quote(part))
	}
	var bm uint64
	for v := start; v <= end; v += step {
		bm |= 1 << v
	}
	return bm, nil
}
