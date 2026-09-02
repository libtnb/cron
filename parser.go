package cron

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// This file implements the standard cron grammar. A spec is an optional
// "TZ=<zone> " or "CRON_TZ=<zone> " prefix followed by either a descriptor
// ("@hourly", "@every 90s", ...) or five fields (minute hour dom month dow),
// six with a leading seconds field when the parser allows it. Each field is a
// comma list of "*", "?", a value, a range "a-b", or any of those with
// "/step"; month and dow accept three-letter names and dow accepts 7 as
// Sunday. Fields compile to bitmasks, and starBit records a literal "*" or
// "?" so SpecSchedule can apply the classic day-of-month/day-of-week
// coupling.

const (
	// starBit marks "*" or "?" for DOM/DOW coupling.
	starBit = uint64(1) << 63

	// minEveryInterval is the smallest interval "@every" accepts; anything
	// shorter is almost certainly a typo and would spin the scheduler.
	minEveryInterval = time.Millisecond
)

// ParserOption configures NewStandardParser.
type ParserOption func(*parserConfig)

// parserConfig is the StandardParser configuration assembled by
// NewStandardParser.
type parserConfig struct {
	seconds       bool
	secondsStrict bool
	ext           Parser
	defaultLoc    *time.Location
}

// WithOptionalSeconds accepts both five- and six-field specs; a five-field
// spec is parsed with second 0. Without it six-field specs are rejected.
func WithOptionalSeconds() ParserOption {
	return func(c *parserConfig) {
		c.seconds = true
	}
}

// WithRequiredSeconds requires exactly six fields with a leading seconds
// field, rejecting five-field specs. It takes precedence over
// WithOptionalSeconds.
func WithRequiredSeconds() ParserOption {
	return func(c *parserConfig) {
		c.seconds = true
		c.secondsStrict = true
	}
}

// WithParserExt consults ext before the standard grammar, for custom
// descriptors or syntax. ext returning (nil, nil) falls through to standard
// parsing; a non-nil error is returned as is. A nil ext is ignored.
func WithParserExt(ext Parser) ParserOption {
	return func(c *parserConfig) {
		if isNilLike(ext) {
			ext = nil
		}
		c.ext = ext
	}
}

// WithDefaultLocation sets the time zone for specs without a TZ=/CRON_TZ=
// prefix. nil (the default) means time.Local.
func WithDefaultLocation(loc *time.Location) ParserOption {
	return func(c *parserConfig) { c.defaultLoc = loc }
}

// StandardParser parses the classic cron grammar. It is immutable after
// construction and safe for concurrent use. New builds one from WithLocation
// and WithSecondsField unless WithParser is set; ValidateSpec and AnalyzeSpec
// use a five-field instance in time.Local.
type StandardParser struct {
	cfg parserConfig
}

// NewStandardParser builds a parser; without options it accepts five-field
// specs and descriptors in time.Local.
func NewStandardParser(opts ...ParserOption) *StandardParser {
	var cfg parserConfig
	for _, o := range opts {
		o(&cfg)
	}
	return &StandardParser{cfg: cfg}
}

// Parse compiles spec into a *SpecSchedule, or a ConstantDelay for "@every".
// Surrounding whitespace is ignored. It returns a *ParseError for an empty
// spec, a wrong field count, an out-of-range or malformed field, an unknown
// descriptor, an unknown or empty TZ= zone, or an "@every" interval below one
// millisecond.
func (p *StandardParser) Parse(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, &ParseError{Spec: spec, Pos: -1, Reason: "empty spec"}
	}
	if p.cfg.ext != nil {
		s, err := p.cfg.ext.Parse(spec)
		if err != nil {
			return nil, err
		}
		if !isNilLike(s) {
			return s, nil
		}
	}

	loc := p.cfg.defaultLoc
	if loc == nil {
		loc = time.Local
	}
	spec, loc, err := stripZone(spec, loc)
	if err != nil {
		return nil, err
	}

	if spec[0] == '@' {
		return p.parseDescriptor(spec, loc)
	}

	fields := strings.Fields(spec)
	hasSeconds, err := p.expectFieldCount(spec, len(fields))
	if err != nil {
		return nil, err
	}
	parseField := func(name, expr string, b boundary) (uint64, error) {
		return getField(
			spec,
			name,
			expr,
			b,
		)
	}

	var idx int
	sec := uint64(1 << 0) // 0
	if hasSeconds {
		v, err := parseField("second", fields[idx], boundsSecond)
		if err != nil {
			return nil, err
		}
		sec = v
		idx++
	}
	min, err := parseField("minute", fields[idx], boundsMinute)
	if err != nil {
		return nil, err
	}
	idx++
	hour, err := parseField("hour", fields[idx], boundsHour)
	if err != nil {
		return nil, err
	}
	idx++
	dom, err := parseField("dom", fields[idx], boundsDom)
	if err != nil {
		return nil, err
	}
	idx++
	month, err := parseField("month", fields[idx], boundsMonth)
	if err != nil {
		return nil, err
	}
	idx++
	dow, err := parseField("dow", fields[idx], boundsDow)
	if err != nil {
		return nil, err
	}
	dow = normalizeDow(dow)

	return &SpecSchedule{
		second: sec,
		minute: min,
		hour:   hour,
		dom:    dom,
		month:  month,
		dow:    dow,
		loc:    loc,
	}, nil
}

// expectFieldCount validates n against the parser's seconds mode and reports
// whether the spec carries a leading seconds field.
func (p *StandardParser) expectFieldCount(spec string, n int) (bool, error) {
	switch {
	case p.cfg.seconds && p.cfg.secondsStrict:
		if n != 6 {
			return false, &ParseError{
				Spec: spec, Pos: -1,
				Reason: "expected 6 fields, got " + strconv.Itoa(n),
			}
		}
		return true, nil
	case p.cfg.seconds:
		switch n {
		case 5:
			return false, nil
		case 6:
			return true, nil
		default:
			return false, &ParseError{
				Spec: spec, Pos: -1,
				Reason: "expected 5 or 6 fields, got " + strconv.Itoa(n),
			}
		}
	default:
		if n != 5 {
			return false, &ParseError{
				Spec: spec, Pos: -1,
				Reason: "expected 5 fields, got " + strconv.Itoa(n),
			}
		}
		return false, nil
	}
}

// parseDescriptor handles "@every <duration>" and the fixed descriptors.
// Descriptor schedules mark every unrestricted field with starBit, exactly as
// a literal "*" would.
func (p *StandardParser) parseDescriptor(spec string, loc *time.Location) (Schedule, error) {
	const everyPrefix = "@every "
	if strings.HasPrefix(spec, everyPrefix) {
		dur, err := time.ParseDuration(strings.TrimSpace(spec[len(everyPrefix):]))
		if err != nil {
			return nil, &ParseError{
				Spec: spec, Field: "@every", Pos: -1,
				Reason: "invalid duration", Err: err,
			}
		}
		if dur < minEveryInterval {
			return nil, &ParseError{
				Spec: spec, Field: "@every", Pos: -1,
				Reason: "duration must be at least " + minEveryInterval.String(),
			}
		}
		return ConstantDelay(dur), nil
	}

	allStar := func(b boundary) uint64 { return rangeAll(b) | starBit }

	switch spec {
	case "@yearly", "@annually":
		return &SpecSchedule{
			second: 1 << 0,
			minute: 1 << 0,
			hour:   1 << 0,
			dom:    1 << 1,
			month:  1 << 1,
			dow:    allStar(boundsDow),
			loc:    loc,
		}, nil
	case "@monthly":
		return &SpecSchedule{
			second: 1 << 0,
			minute: 1 << 0,
			hour:   1 << 0,
			dom:    1 << 1,
			month:  allStar(boundsMonth),
			dow:    allStar(boundsDow),
			loc:    loc,
		}, nil
	case "@weekly":
		return &SpecSchedule{
			second: 1 << 0,
			minute: 1 << 0,
			hour:   1 << 0,
			dom:    allStar(boundsDom),
			month:  allStar(boundsMonth),
			dow:    1 << 0, // Sunday
			loc:    loc,
		}, nil
	case "@daily", "@midnight":
		return &SpecSchedule{
			second: 1 << 0,
			minute: 1 << 0,
			hour:   1 << 0,
			dom:    allStar(boundsDom),
			month:  allStar(boundsMonth),
			dow:    allStar(boundsDow),
			loc:    loc,
		}, nil
	case "@hourly":
		return &SpecSchedule{
			second: 1 << 0,
			minute: 1 << 0,
			hour:   allStar(boundsHour),
			dom:    allStar(boundsDom),
			month:  allStar(boundsMonth),
			dow:    allStar(boundsDow),
			loc:    loc,
		}, nil
	}

	return nil, &ParseError{Spec: spec, Pos: 0, Reason: "unrecognized descriptor"}
}

// boundary is the value range of one field plus the names it accepts.
type boundary struct {
	min, max uint
	names    map[string]uint
}

var (
	boundsSecond = boundary{min: 0, max: 59}
	boundsMinute = boundary{min: 0, max: 59}
	boundsHour   = boundary{min: 0, max: 23}
	boundsDom    = boundary{min: 1, max: 31}
	boundsMonth  = boundary{min: 1, max: 12, names: map[string]uint{
		"jan": 1,
		"feb": 2,
		"mar": 3,
		"apr": 4,
		"may": 5,
		"jun": 6,
		"jul": 7,
		"aug": 8,
		"sep": 9,
		"oct": 10,
		"nov": 11,
		"dec": 12,
	}}
	// dow accepts 0-7 per POSIX; 7 is folded into Sunday after parsing.
	boundsDow = boundary{min: 0, max: 7, names: map[string]uint{
		"sun": 0,
		"mon": 1,
		"tue": 2,
		"wed": 3,
		"thu": 4,
		"fri": 5,
		"sat": 6,
	}}
)

// rangeAll returns the bitmask with every value of b set.
func rangeAll(b boundary) uint64 {
	var bm uint64
	for v := b.min; v <= b.max; v++ {
		bm |= 1 << v
	}
	return bm
}

// stripZone resolves and removes a leading "TZ=<zone> " or "CRON_TZ=<zone> "
// prefix from an already trimmed spec. Specs without a prefix come back
// unchanged with fallback.
func stripZone(spec string, fallback *time.Location) (string, *time.Location, error) {
	i := strings.IndexByte(spec, ' ')
	if i <= 0 {
		return spec, fallback, nil
	}
	key, zone, ok := strings.Cut(spec[:i], "=")
	if !ok || key == "" {
		return spec, fallback, nil
	}
	if key != "TZ" && key != "CRON_TZ" {
		return spec, fallback, nil
	}
	if zone == "" {
		return "", nil, &ParseError{Spec: spec, Field: key, Pos: 0, Reason: "empty time zone"}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", nil, &ParseError{
			Spec:   spec,
			Field:  key,
			Pos:    0,
			Reason: "unknown time zone " + strconv.Quote(zone),
			Err:    err,
		}
	}
	return strings.TrimSpace(spec[i+1:]), loc, nil
}

// getField compiles a comma-separated field expression into a bitmask.
func getField(spec, name, expr string, b boundary) (uint64, error) {
	var bm uint64
	for part := range strings.SplitSeq(expr, ",") {
		v, err := getRange(
			spec,
			name,
			part,
			b,
		)
		if err != nil {
			return 0, err
		}
		bm |= v
	}
	return bm, nil
}

// getRange compiles one part of a field: "*", "?", "n", "a-b", or any of
// those followed by "/step".
func getRange(spec, name, expr string, b boundary) (uint64, error) {
	var (
		start, end, step uint
		bits             uint64
		err              error
		extra            uint64 // starBit if expr was "*" or "?"
	)

	rangeAndStep := strings.Split(expr, "/")
	switch len(rangeAndStep) {
	case 1:
		step = 1
	case 2:
		s, err := strconv.ParseUint(rangeAndStep[1], 10, 32)
		if err != nil || s == 0 {
			return 0, fieldError(spec, name, "invalid step "+strconv.Quote(rangeAndStep[1]))
		}
		step = uint(s)
	default:
		return 0, fieldError(spec, name, "too many slashes in "+strconv.Quote(expr))
	}

	lowHigh := rangeAndStep[0]
	switch lowHigh {
	case "*", "?":
		start, end = b.min, b.max
		extra = starBit
	default:
		lh := strings.Split(lowHigh, "-")
		switch len(lh) {
		case 1:
			start, err = parseIntOrName(lh[0], b)
			if err != nil {
				return 0, fieldError(spec, name, err.Error())
			}
			// "n/step" runs from n to the field maximum; a bare "n" is just n.
			end = start
			if len(rangeAndStep) == 2 {
				end = b.max
			}
		case 2:
			start, err = parseIntOrName(lh[0], b)
			if err != nil {
				return 0, fieldError(spec, name, err.Error())
			}
			end, err = parseIntOrName(lh[1], b)
			if err != nil {
				return 0, fieldError(spec, name, err.Error())
			}
		default:
			return 0, fieldError(spec, name, "too many dashes in "+strconv.Quote(lowHigh))
		}
	}

	if start < b.min {
		reason := strconv.Itoa(int(start)) + " below minimum " + strconv.Itoa(int(b.min))
		return 0, fieldError(spec, name, reason)
	}
	if end > b.max {
		reason := strconv.Itoa(int(end)) + " above maximum " + strconv.Itoa(int(b.max))
		return 0, fieldError(spec, name, reason)
	}
	if start > end {
		reason := "beginning of range " + strconv.Itoa(int(start)) + " beyond end " + strconv.Itoa(int(end))
		return 0, fieldError(spec, name, reason)
	}

	// Iterate in uint64 so a huge step near 2^32 cannot wrap on 32-bit
	// platforms (where uint is 32-bit) and set spurious bits; v stays <= 59.
	for v := uint64(start); v <= uint64(end); v += uint64(step) {
		bits |= 1 << v
	}
	return bits | extra, nil
}

// fieldError builds the ParseError for a fault inside one field.
func fieldError(spec, name, reason string) *ParseError {
	return &ParseError{Spec: spec, Field: name, Pos: -1, Reason: reason}
}

// normalizeDow folds bit 7 (the POSIX alias for Sunday) into bit 0.
func normalizeDow(bm uint64) uint64 {
	if bm&(1<<7) != 0 {
		bm = bm&^(1<<7) | 1
	}
	return bm
}

// parseIntOrName resolves a number or, when b has names, a case-insensitive
// name such as "mon" or "jan".
func parseIntOrName(s string, b boundary) (uint, error) {
	if b.names != nil {
		if v, ok := b.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, errors.New("not a valid number or name: " + strconv.Quote(s))
	}
	return uint(v), nil
}
