package cron

import (
	"errors"
	"strings"
	"time"
)

// SpecAnalysis is the result of AnalyzeSpec. Most fields are populated
// only when Valid is true.
type SpecAnalysis struct {
	Spec        string
	Valid       bool
	Err         error
	IsTriggered bool
	Descriptor  string         // "@every", "@hourly", ... or "" for 5/6-field specs
	Interval    time.Duration  // set when Descriptor == "@every"
	Location    *time.Location // schedule timezone
	NextRun     time.Time      // upcoming firing relative to the now passed in
}

// ValidateSpec checks spec with the standard parser.
func ValidateSpec(spec string) error {
	return ValidateSpecWith(spec, defaultParser)
}

// ValidateSpecWith checks spec with p.
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

// AnalyzeSpec describes spec relative to now using the standard parser.
func AnalyzeSpec(spec string, now time.Time) SpecAnalysis {
	return AnalyzeSpecWith(spec, defaultParser, now)
}

// AnalyzeSpecWith describes spec relative to now using p.
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
		// Report the effective interval: ConstantDelay enforces a 1s floor, so a
		// sub-second @every fires every 1s. Reporting the raw duration would
		// contradict the actual cadence.
		res.Interval = max(time.Duration(v), time.Second)
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

var defaultParser = NewStandardParser()

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
