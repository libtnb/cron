package cron

import (
	"errors"
	"strings"
	"time"
)

// defaultParser backs ValidateSpec and AnalyzeSpec: five fields, time.Local.
var defaultParser = NewStandardParser()

// SpecAnalysis is the result of AnalyzeSpec. When Valid is false only Spec and
// Err are meaningful.
type SpecAnalysis struct {
	Spec        string
	Valid       bool
	Err         error          // *ParseError or ErrNilSchedule when Valid is false
	IsTriggered bool           // the spec parsed to TriggeredSchedule (custom parsers only)
	Descriptor  string         // "@every", "@hourly", ... or "" for field specs
	Interval    time.Duration  // set when Descriptor == "@every"
	Location    *time.Location // schedule time zone when the Schedule exposes one; nil otherwise
	NextRun     time.Time      // first firing after the now passed in; zero if none or IsTriggered
}

// ValidateSpec checks spec against the built-in five-field parser in
// time.Local; use ValidateSpecWith to match a scheduler configured with
// WithSecondsField or WithParser. Returns a *ParseError describing the fault,
// or nil.
func ValidateSpec(spec string) error {
	return ValidateSpecWith(spec, defaultParser)
}

// ValidateSpecWith checks spec with p. Returns p's error (a *ParseError for
// the built-in parsers), ErrNilSchedule when p returns no schedule, or an
// error for a nil p.
func ValidateSpecWith(spec string, p Parser) error {
	if isNilLike(p) {
		return errors.New("cron: nil parser")
	}
	s, err := p.Parse(spec)
	if err != nil {
		return err
	}
	if isNilLike(s) {
		return ErrNilSchedule
	}
	return nil
}

// AnalyzeSpec describes spec relative to now using the built-in five-field
// parser in time.Local. It never fails: a rejected spec is reported through
// SpecAnalysis.Valid and Err. Use AnalyzeSpecWith to match a custom parser.
func AnalyzeSpec(spec string, now time.Time) SpecAnalysis {
	return AnalyzeSpecWith(spec, defaultParser, now)
}

// AnalyzeSpecWith describes spec relative to now using p. Location is set
// when the Schedule has a Location() *time.Location method, as SpecSchedule
// and parserext.QuartzSchedule do.
func AnalyzeSpecWith(spec string, p Parser, now time.Time) SpecAnalysis {
	res := SpecAnalysis{Spec: spec}
	if isNilLike(p) {
		res.Err = errors.New("cron: nil parser")
		return res
	}
	s, err := p.Parse(spec)
	if err != nil {
		res.Err = err
		return res
	}
	if isNilLike(s) {
		res.Err = ErrNilSchedule
		return res
	}
	res.Valid = true
	res.IsTriggered = IsTriggered(s)
	res.Descriptor = extractDescriptor(spec)

	if v, ok := s.(ConstantDelay); ok {
		res.Interval = time.Duration(v)
	}
	// Duck-typed so validate.go does not import parserext.
	type locationProvider interface{ Location() *time.Location }
	if lp, ok := s.(locationProvider); ok {
		res.Location = lp.Location()
	}

	if !res.IsTriggered {
		res.NextRun = s.Next(now)
	}
	return res
}

// extractDescriptor returns the "@word" that starts spec after any TZ=/CRON_TZ=
// prefix, or "" for field specs.
func extractDescriptor(spec string) string {
	s := strings.TrimSpace(spec)
	if i := strings.IndexByte(s, ' '); i > 0 {
		head := s[:i]
		if eq := strings.IndexByte(head, '='); eq > 0 {
			key := head[:eq]
			if key == "TZ" || key == "CRON_TZ" {
				s = strings.TrimSpace(s[i+1:])
			}
		}
	}
	if !strings.HasPrefix(s, "@") {
		return ""
	}
	end := len(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		end = i
	}
	return s[:end]
}
