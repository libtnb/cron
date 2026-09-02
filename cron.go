package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libtnb/cron/internal/heap"
	"github.com/libtnb/cron/internal/parsecache"
)

// defaultParseCacheLimit caps how many distinct specs Add memoizes.
const defaultParseCacheLimit = 1024

// Cron is a job scheduler. Construct one with New, register jobs with Add or
// AddSchedule, then call Start. All methods are safe for concurrent use.
//
// The loop goroutine only pops due entries. Schedule.Next, distributed
// coordination and jobs all run on their own goroutines, so one slow schedule
// or job never delays another entry.
type Cron struct {
	cfg        config
	parseCache parsecache.Cache[Schedule]

	mu      sync.Mutex         // guards h, byEntry, byKey and entry mutation
	h       *heap.Heap[*entry] // scheduling heap
	byEntry map[EntryID]*entry // canonical entry table
	byKey   map[string]EntryID // non-empty stable keys, unique within the scheduler
	nextID  atomic.Uint64

	viewMu sync.RWMutex // guards the views map structure; cell values are atomic
	views  viewMap      // snapshot map read by Entry/Entries

	events *eventBus

	running atomic.Bool
	wakeCh  chan struct{}

	startMu    sync.Mutex
	runCtx     context.Context
	runCancel  context.CancelCauseFunc
	loopCancel context.CancelFunc // stops the loop without cancelling jobs
	runDone    chan struct{}
	started    bool

	wg       sync.WaitGroup // fire planners and job invocations
	inflight atomic.Int64
	jobsDone chan struct{}
	jobsOnce sync.Once
}

// New constructs a Cron from opts; nothing fires until Start is called.
//
// Defaults: time.Local, slog.Default(), the five-field standard parser,
// MissedRunOnce with a one-minute tolerance, no jitter, no retry, unlimited
// concurrency and entries, and job panics recovered into ErrJobPanic.
//
// Returns an error wrapping ErrInvalidOption when an option is nil or rejects
// its argument. WithLocation is ignored, with a warning, when WithParser is
// also set: a custom parser owns time-zone resolution.
func New(opts ...Option) (*Cron, error) {
	var cfg config
	for i, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i)
		}
		if err := o(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.loc == nil {
		cfg.loc = time.Local
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	switch {
	case cfg.parser == nil:
		popts := []ParserOption{WithDefaultLocation(cfg.loc)}
		if cfg.secondsField {
			popts = append(popts, WithOptionalSeconds())
		}
		cfg.parser = NewStandardParser(popts...)
	case cfg.locSet:
		cfg.logger.Warn("cron: WithLocation ignored; the parser from WithParser controls the timezone")
	}
	if cfg.missedTolerance <= 0 {
		cfg.missedTolerance = defaultMissedTolerance
	}

	c := &Cron{
		cfg:      cfg,
		h:        heap.New[*entry](),
		byEntry:  make(map[EntryID]*entry),
		byKey:    make(map[string]EntryID),
		views:    make(viewMap),
		wakeCh:   make(chan struct{}, 1),
		jobsDone: make(chan struct{}),
	}
	c.parseCache.Limit = defaultParseCacheLimit
	c.events = newEventBus(
		cfg.observers,
		cfg.recorder,
		cfg.logger,
		cfg.observerBuffer,
	)
	return c, nil
}

// MustNew is New that panics instead of returning an error, for configuration
// that is fixed at build time.
func MustNew(opts ...Option) *Cron {
	c, err := New(opts...)
	if err != nil {
		panic(err)
	}
	return c
}

// parse memoizes parser results per spec so repeated Add calls with the same
// expression share one Schedule. Failures and nil results are evicted so a
// caller-controlled invalid spec cannot occupy the cache.
func (c *Cron) parse(spec string) (Schedule, error) {
	s, err := c.parseCache.Get(spec, func() (Schedule, error) {
		return c.cfg.parser.Parse(spec)
	})
	if err != nil {
		c.parseCache.Forget(spec)
		return nil, err
	}
	if isNilLike(s) {
		c.parseCache.Forget(spec)
		return nil, ErrNilSchedule
	}
	return s, nil
}
