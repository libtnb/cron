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

// ValidateSpec returns nil iff spec parses with the standard parser.
func ValidateSpec(spec string) error {
	return ValidateSpecWith(spec, defaultParser)
}

// ValidateSpecWith is ValidateSpec with a custom parser.
func ValidateSpecWith(spec string, p Parser) error {
	if p == nil {
		return errors.New("cron: nil parser")
	}
	_, err := p.Parse(spec)
	return err
}

// AnalyzeSpec parses spec and returns a structured description.
func AnalyzeSpec(spec string, now time.Time) SpecAnalysis {
	return AnalyzeSpecWith(spec, defaultParser, now)
}

// AnalyzeSpecWith is AnalyzeSpec with a custom parser.
func AnalyzeSpecWith(spec string, p Parser, now time.Time) SpecAnalysis {
	res := SpecAnalysis{Spec: spec}
	if p == nil {
		res.Err = errors.New("cron: nil parser")
		return res
	}
	s, err := p.Parse(spec)
	if err != nil {
		res.Err = err
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
