package cron

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// starBit marks "*" for DOM/DOW coupling.
const starBit = uint64(1) << 63

// ParserOption configures NewStandardParser.
type ParserOption func(*parserConfig)

// WithSeconds enables a leading seconds field (6-field form).
func WithSeconds() ParserOption {
	return func(c *parserConfig) { c.seconds = true }
}

// WithParserExt installs a pre-parse hook. Returning (nil, nil) falls through
// to the standard parser.
func WithParserExt(ext Parser) ParserOption {
	return func(c *parserConfig) { c.ext = ext }
}

// WithDefaultLocation sets the default timezone for specs without
// TZ=/CRON_TZ=. nil means time.Local.
func WithDefaultLocation(loc *time.Location) ParserOption {
	return func(c *parserConfig) { c.defaultLoc = loc }
}

type parserConfig struct {
	seconds    bool
	ext        Parser
	defaultLoc *time.Location
}

// StandardParser is stateless and concurrent-safe.
type StandardParser struct {
	cfg parserConfig
}

// NewStandardParser handles 5/6-field specs, descriptors, and TZ prefixes.
func NewStandardParser(opts ...ParserOption) *StandardParser {
	cfg := parserConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	return &StandardParser{cfg: cfg}
}

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
		if s != nil {
			return s, nil
		}
	}

	loc := p.cfg.defaultLoc
	if loc == nil {
		loc = time.Local
	}
	if i := strings.IndexByte(spec, ' '); i > 0 {
		head := spec[:i]
		if eq := strings.IndexByte(head, '='); eq > 0 {
			key := head[:eq]
			if key == "TZ" || key == "CRON_TZ" {
				zone := head[eq+1:]
				l, err := time.LoadLocation(zone)
				if err != nil {
					return nil, &ParseError{
						Spec: spec, Field: key, Pos: 0,
						Reason: "unknown time zone " + strconv.Quote(zone),
						Err:    err,
					}
				}
				loc = l
				spec = strings.TrimSpace(spec[i+1:])
				if spec == "" {
					return nil, &ParseError{Spec: spec, Pos: -1, Reason: "empty spec after timezone"}
				}
			}
		}
	}

	if spec[0] == '@' {
		return p.parseDescriptor(spec, loc)
	}

	fields := strings.Fields(spec)
	want := 5
	if p.cfg.seconds {
		want = 6
	}
	if len(fields) != want {
		return nil, &ParseError{
			Spec:   spec,
			Pos:    -1,
			Reason: "expected " + strconv.Itoa(want) + " fields, got " + strconv.Itoa(len(fields)),
		}
	}

	idx := 0
	var sec uint64
	if p.cfg.seconds {
		v, err := getField(spec, "second", fields[idx], boundsSecond)
		if err != nil {
			return nil, err
		}
		sec = v
		idx++
	} else {
		sec = 1 << 0 // 0
	}
	min, err := getField(spec, "minute", fields[idx], boundsMinute)
	if err != nil {
		return nil, err
	}
	idx++
	hour, err := getField(spec, "hour", fields[idx], boundsHour)
	if err != nil {
		return nil, err
	}
	idx++
	dom, err := getField(spec, "dom", fields[idx], boundsDom)
	if err != nil {
		return nil, err
	}
	idx++
	month, err := getField(spec, "month", fields[idx], boundsMonth)
	if err != nil {
		return nil, err
	}
	idx++
	dow, err := getField(spec, "dow", fields[idx], boundsDow)
	if err != nil {
		return nil, err
	}

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
		if dur <= 0 {
			return nil, &ParseError{Spec: spec, Field: "@every", Pos: -1, Reason: "duration must be > 0"}
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

type boundary struct {
	min, max uint
	names    map[string]uint
}

func rangeAll(b boundary) uint64 {
	var bm uint64
	for v := b.min; v <= b.max; v++ {
		bm |= 1 << v
	}
	return bm
}

var (
	boundsSecond = boundary{0, 59, nil}
	boundsMinute = boundary{0, 59, nil}
	boundsHour   = boundary{0, 23, nil}
	boundsDom    = boundary{1, 31, nil}
	boundsMonth  = boundary{1, 12, map[string]uint{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	boundsDow = boundary{0, 6, map[string]uint{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

func getField(spec, name, expr string, b boundary) (uint64, error) {
	if expr == "" {
		return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: "empty field"}
	}
	var bm uint64
	for part := range strings.SplitSeq(expr, ",") {
		v, err := getRange(spec, name, part, b)
		if err != nil {
			return 0, err
		}
		bm |= v
	}
	return bm, nil
}

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
			return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: "invalid step " + strconv.Quote(rangeAndStep[1])}
		}
		step = uint(s)
	default:
		return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: "too many slashes in " + strconv.Quote(expr)}
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
				return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: err.Error()}
			}
			if len(rangeAndStep) == 2 {
				end = b.max
			} else {
				end = start
			}
		case 2:
			start, err = parseIntOrName(lh[0], b)
			if err != nil {
				return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: err.Error()}
			}
			end, err = parseIntOrName(lh[1], b)
			if err != nil {
				return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: err.Error()}
			}
		default:
			return 0, &ParseError{Spec: spec, Field: name, Pos: -1, Reason: "too many dashes in " + strconv.Quote(lowHigh)}
		}
	}

	if start < b.min {
		return 0, &ParseError{Spec: spec, Field: name, Pos: -1,
			Reason: strconv.Itoa(int(start)) + " below minimum " + strconv.Itoa(int(b.min))}
	}
	if end > b.max {
		return 0, &ParseError{Spec: spec, Field: name, Pos: -1,
			Reason: strconv.Itoa(int(end)) + " above maximum " + strconv.Itoa(int(b.max))}
	}
	if start > end {
		return 0, &ParseError{Spec: spec, Field: name, Pos: -1,
			Reason: "beginning of range " + strconv.Itoa(int(start)) + " beyond end " + strconv.Itoa(int(end))}
	}

	for v := start; v <= end; v += step {
		bits |= 1 << v
	}
	return bits | extra, nil
}

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
