// Package workflow runs DAGs of cron.Jobs.
package workflow

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/libtnb/cron"
)

var (
	ErrDuplicateStep = errors.New("workflow: duplicate step")
	ErrUnknownDep    = errors.New("workflow: unknown dependency")
	ErrCycle         = errors.New("workflow: cycle detected")
	ErrNilJob        = errors.New("workflow: step has no job")
)

type ConfigError struct {
	Err  error
	Step string
	Dep  string // ErrUnknownDep / ErrCycle only
}

func (e *ConfigError) Error() string {
	switch {
	case errors.Is(e.Err, ErrDuplicateStep), errors.Is(e.Err, ErrNilJob):
		return fmt.Sprintf("%v: %q", e.Err, e.Step)
	case errors.Is(e.Err, ErrUnknownDep):
		return fmt.Sprintf("%v: step %q depends on %q", e.Err, e.Step, e.Dep)
	case errors.Is(e.Err, ErrCycle):
		return fmt.Sprintf("%v: %q -> %q", e.Err, e.Step, e.Dep)
	default:
		return e.Err.Error()
	}
}

func (e *ConfigError) Unwrap() error { return e.Err }

// Result is a Step outcome.
type Result uint8

const (
	ResultPending Result = iota // never appears in a finished Execution
	ResultSuccess
	ResultFailure // Job returned an error or panicked
	ResultSkipped // a dep didn't satisfy this step's Condition, or ctx was cancelled
)

func (r Result) String() string {
	switch r {
	case ResultPending:
		return "pending"
	case ResultSuccess:
		return "success"
	case ResultFailure:
		return "failure"
	case ResultSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Condition selects which upstream outcome triggers a Step.
type Condition uint8

const (
	OnSuccess  Condition = iota // upstream succeeded
	OnFailure                   // upstream failed
	OnSkipped                   // upstream was skipped
	OnComplete                  // any terminal state
)

func (c Condition) match(r Result) bool {
	switch c {
	case OnSuccess:
		return r == ResultSuccess
	case OnFailure:
		return r == ResultFailure
	case OnSkipped:
		return r == ResultSkipped
	case OnComplete:
		return r != ResultPending
	default:
		return false
	}
}

// Dep is one DAG edge.
type Dep struct {
	Name string
	When Condition
}

func After(name string, when Condition) Dep { return Dep{Name: name, When: when} }

// Step is one node in the DAG. Exactly one of Job or Fn must be set.
type Step struct {
	Name    string
	Job     cron.Job
	Fn      StepFunc // alternative to Job; receives dependency outputs
	Deps    []Dep
	Timeout time.Duration    // per-run cap; zero means none
	Retry   cron.RetryPolicy // per-run retry; zero means none
}

// Inputs holds dependency outputs keyed by step name. Plain-Job, failed, and
// skipped dependencies yield nil.
type Inputs map[string]any

// StepFunc is a step body that consumes dependency outputs and produces its
// own, letting data flow through the DAG.
type StepFunc func(ctx context.Context, in Inputs) (any, error)

func NewStep(name string, job cron.Job, deps ...Dep) Step {
	return Step{Name: name, Job: job, Deps: deps}
}

// NewStepFunc is NewStep for a StepFunc whose output feeds dependent steps.
func NewStepFunc(name string, fn StepFunc, deps ...Dep) Step {
	return Step{Name: name, Fn: fn, Deps: deps}
}

// WithTimeout returns a copy of s capped at d per run, with cron.ErrJobTimeout
// as the cancellation cause.
func (s Step) WithTimeout(d time.Duration) Step {
	s.Timeout = d
	return s
}

// WithRetry returns a copy of s retried per p on error.
func (s Step) WithRetry(p cron.RetryPolicy) Step {
	s.Retry = p
	return s
}

// Workflow is a DAG of Steps.
type Workflow struct {
	steps      map[string]Step
	order      []string
	onComplete func(*Execution)
}

// New constructs a Workflow. It copies Step.Deps and returns *ConfigError for
// duplicate steps, unknown dependencies, or cycles.
func New(steps ...Step) (*Workflow, error) {
	w := &Workflow{
		steps: make(map[string]Step, len(steps)),
		order: make([]string, 0, len(steps)),
	}
	for _, s := range steps {
		if _, dup := w.steps[s.Name]; dup {
			return nil, &ConfigError{Err: ErrDuplicateStep, Step: s.Name}
		}
		if s.Job == nil && s.Fn == nil {
			return nil, &ConfigError{Err: ErrNilJob, Step: s.Name}
		}
		copied := s
		if len(s.Deps) > 0 {
			copied.Deps = append([]Dep(nil), s.Deps...)
		}
		w.steps[s.Name] = copied
		w.order = append(w.order, s.Name)
	}
	for _, name := range w.order {
		for _, d := range w.steps[name].Deps {
			if _, ok := w.steps[d.Name]; !ok {
				return nil, &ConfigError{Err: ErrUnknownDep, Step: name, Dep: d.Name}
			}
		}
	}
	if err := w.validateAcyclic(); err != nil {
		return nil, err
	}
	return w, nil
}

// MustNew panics on configuration error.
func MustNew(steps ...Step) *Workflow {
	w, err := New(steps...)
	if err != nil {
		panic(err)
	}
	return w
}

func (w *Workflow) validateAcyclic() error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(w.steps))
	var firstCycle *ConfigError
	var visit func(name string)
	visit = func(name string) {
		color[name] = gray
		for _, d := range w.steps[name].Deps {
			switch color[d.Name] {
			case gray:
				firstCycle = &ConfigError{Err: ErrCycle, Step: name, Dep: d.Name}
				return
			case white:
				visit(d.Name)
				if firstCycle != nil {
					return
				}
			}
		}
		color[name] = black
	}
	for _, name := range w.order {
		if color[name] == white {
			visit(name)
			if firstCycle != nil {
				return firstCycle
			}
		}
	}
	return nil
}

// WithOnComplete returns a shallow copy with cb installed. cb runs before Run
// returns.
func (w *Workflow) WithOnComplete(cb func(*Execution)) *Workflow {
	nw := *w
	nw.onComplete = cb
	return &nw
}

// StepReport is one step's outcome with timing and output.
type StepReport struct {
	Result    Result
	Err       error
	Output    any       // StepFunc return value; nil for plain Jobs
	StartedAt time.Time // zero if the step never ran
	Duration  time.Duration
}

// Execution is the result of one Workflow run.
type Execution struct {
	ID        string
	StartedAt time.Time
	Duration  time.Duration
	Results   map[string]Result
	Errors    map[string]error
	Steps     map[string]StepReport
}

// Err joins recorded Step errors.
func (e *Execution) Err() error {
	var errs []error
	for _, err := range e.Errors {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run executes the DAG once and returns Execution.Err.
func (w *Workflow) Run(ctx context.Context) error {
	exec := w.execute(ctx)
	if w.onComplete != nil {
		w.onComplete(exec)
	}
	return exec.Err()
}

type stepState struct {
	done      chan struct{}
	result    Result
	err       error
	output    any
	startedAt time.Time
	duration  time.Duration
}

func (w *Workflow) execute(ctx context.Context) *Execution {
	begin := time.Now()
	states := make(map[string]*stepState, len(w.steps))
	for _, name := range w.order {
		states[name] = &stepState{done: make(chan struct{})}
	}

	finalize := func(name string, result Result, err error) {
		st := states[name]
		st.result = result
		st.err = err
		close(st.done)
	}

	runStep := func(name string) {
		if err := ctx.Err(); err != nil {
			finalize(name, ResultSkipped, err)
			return
		}
		s := w.steps[name]
		for _, d := range s.Deps {
			select {
			case <-states[d.Name].done:
			case <-ctx.Done():
				finalize(name, ResultSkipped, ctx.Err())
				return
			}
		}
		for _, d := range s.Deps {
			if !d.When.match(states[d.Name].result) {
				finalize(name, ResultSkipped, nil)
				return
			}
		}

		st := states[name]
		st.startedAt = time.Now()
		var out any
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("workflow: step %q panicked: %v", name, r)
				}
			}()
			runCtx := ctx
			if s.Timeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeoutCause(ctx, s.Timeout, cron.ErrJobTimeout)
				defer cancel()
			}
			job := s.Job
			if s.Fn != nil {
				in := make(Inputs, len(s.Deps))
				for _, d := range s.Deps {
					in[d.Name] = states[d.Name].output
				}
				job = cron.JobFunc(func(c context.Context) error {
					o, err := s.Fn(c, in)
					if err != nil {
						return err
					}
					out = o
					return nil
				})
			}
			if !s.Retry.IsZero() {
				job = s.Retry.Wrapper()(job)
			}
			return job.Run(runCtx)
		}()
		st.duration = time.Since(st.startedAt)
		if err == nil {
			st.output = out
			finalize(name, ResultSuccess, nil)
		} else {
			finalize(name, ResultFailure, err)
		}
	}

	for _, name := range w.order {
		go runStep(name)
	}
	for _, st := range states {
		<-st.done
	}

	exec := &Execution{
		ID:        genID(),
		StartedAt: begin,
		Duration:  time.Since(begin),
		Results:   make(map[string]Result, len(states)),
		Errors:    make(map[string]error, len(states)),
		Steps:     make(map[string]StepReport, len(states)),
	}
	for name, st := range states {
		exec.Results[name] = st.result
		if st.err != nil {
			exec.Errors[name] = st.err
		}
		exec.Steps[name] = StepReport{
			Result:    st.result,
			Err:       st.err,
			Output:    st.output,
			StartedAt: st.startedAt,
			Duration:  st.duration,
		}
	}
	return exec
}

func genID() string {
	var b [16]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}
