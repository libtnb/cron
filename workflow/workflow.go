// Package workflow builds typed directed acyclic graphs of cron jobs and runs
// them as one cron.Job.
//
// A Builder collects steps: typed functions (Builder.Step) or plain cron.Job
// values (Builder.Job). After declares a dependency on another step's Output
// together with the upstream result that must hold (OnSuccess, OnFailure,
// OnSkipped or OnComplete). Build validates names, dependencies and cycles and
// freezes the graph into an immutable Workflow. Execute runs ready steps with
// bounded parallelism, hands each step the successful outputs of its
// dependencies through Inputs, and reports every step's outcome in an
// Execution.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"
	"uuid"

	"github.com/libtnb/cron"
)

// This file implements the DAG executor. Build compiles the steps into an
// index-addressed graph (dependencies, dependents, a DFS cycle check) so that
// Execute needs no name lookups. Execute is a ready-queue scheduler: steps
// with no pending dependencies are queued and run on their own goroutines, at
// most maxParallelism at a time; each completion decrements its dependents'
// counters and queues those that reach zero. A step whose conditions are not
// met, or whose context is already cancelled when it becomes ready, is
// skipped without running; either way its dependents are released, so the
// loop terminates once every step has a result.

// defaultMaxParallelism bounds concurrently running steps unless
// WithMaxParallelism says otherwise.
const defaultMaxParallelism = 32

// Sentinel errors reported by Build and Execute; match them with errors.Is.
var (
	// ErrDuplicateStep reports two steps with the same name.
	ErrDuplicateStep = errors.New("workflow: duplicate step")
	// ErrDuplicateDep reports the same dependency declared twice on one step.
	ErrDuplicateDep = errors.New("workflow: duplicate dependency")
	// ErrUnknownDep reports a dependency on a step that is not in the graph,
	// including an Output taken from another Builder.
	ErrUnknownDep = errors.New("workflow: unknown dependency")
	// ErrCycle reports a dependency cycle; the message lists its path.
	ErrCycle = errors.New("workflow: dependency cycle")
	// ErrNilJob reports a step added with a nil function or cron.Job.
	ErrNilJob = errors.New("workflow: step has no job")
	// ErrInvalidName reports an empty step name or one with surrounding
	// whitespace.
	ErrInvalidName = errors.New("workflow: invalid step name")
	// ErrInvalidOption is wrapped when a Builder or step option is nil or
	// rejects its argument.
	ErrInvalidOption = errors.New("workflow: invalid option")
	// ErrBuilderFrozen is returned by Build when steps were added after an
	// earlier Build.
	ErrBuilderFrozen = errors.New("workflow: builder is frozen")
	// ErrNilContext is reported by Execution.Err when Execute received a nil
	// context; every step is skipped.
	ErrNilContext = errors.New("workflow: nil context")
)

// ConfigError identifies the step, and for dependency faults the dependency,
// involved in an invalid graph. Err is one of ErrDuplicateStep, ErrNilJob,
// ErrInvalidName, ErrUnknownDep or ErrDuplicateDep.
type ConfigError struct {
	Err  error
	Step string
	Dep  string
}

// Error names the step and, for dependency faults, the dependency.
func (e *ConfigError) Error() string {
	switch {
	case errors.Is(e.Err, ErrDuplicateStep), errors.Is(e.Err, ErrNilJob), errors.Is(e.Err, ErrInvalidName):
		return fmt.Sprintf("%v: %q", e.Err, e.Step)
	case errors.Is(e.Err, ErrUnknownDep), errors.Is(e.Err, ErrDuplicateDep):
		return fmt.Sprintf("%v: step %q depends on %q", e.Err, e.Step, e.Dep)
	default:
		return e.Err.Error()
	}
}

// Unwrap exposes the sentinel to errors.Is.
func (e *ConfigError) Unwrap() error { return e.Err }

// Result is a step outcome as reported by Execution.
type Result uint8

const (
	// ResultPending is a step's state before it completes; it never appears
	// in a finished Execution.
	ResultPending Result = iota
	// ResultSuccess reports that the step returned no error.
	ResultSuccess
	// ResultFailure reports that the step returned an error or panicked.
	ResultFailure
	// ResultSkipped reports that the step did not run because a dependency
	// condition was not met or the context was already cancelled.
	ResultSkipped
)

// String returns "pending", "success", "failure", "skipped" or "unknown".
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

// Condition selects which upstream Result satisfies a dependency declared
// with After.
type Condition uint8

const (
	// The zero Condition is invalid; After rejects it with ErrInvalidOption.
	_ Condition = iota
	// OnSuccess runs the step only if the dependency succeeded.
	OnSuccess
	// OnFailure runs the step only if the dependency failed or panicked.
	OnFailure
	// OnSkipped runs the step only if the dependency was skipped.
	OnSkipped
	// OnComplete runs the step whatever the dependency's result.
	OnComplete
)

func (c Condition) valid() bool { return c >= OnSuccess && c <= OnComplete }

// match reports whether result satisfies c.
func (c Condition) match(result Result) bool {
	switch c {
	case OnSuccess:
		return result == ResultSuccess
	case OnFailure:
		return result == ResultFailure
	case OnSkipped:
		return result == ResultSkipped
	case OnComplete:
		return result != ResultPending
	default:
		return false
	}
}

// Unit is the output type of steps added with Builder.Job; it carries no
// data.
type Unit struct{}

// graphID is a unique token per Builder so an Output cannot be used with a
// graph it does not belong to.
type graphID struct{}

// Output identifies one step's typed output within its graph. Pass it to
// After to declare a dependency and to Inputs.Get or Execution.Get to read
// the value. The zero Output belongs to no graph and is rejected everywhere.
type Output[T any] struct {
	graph *graphID
	name  string
}

// Name returns the step name.
func (o Output[T]) Name() string { return o.name }

// outputValue boxes a step result so Get can check the exact type.
type outputValue[T any] struct{ value T }

// Inputs gives a running step the outputs of its declared dependencies. Only
// successful dependencies carry a value: Get reports ok == false for a
// dependency that failed or was skipped, for a step the caller did not
// declare with After, and for an Output from another graph.
type Inputs struct {
	graph   *graphID
	allowed map[string]struct{}
	values  map[string]any
}

// Get resolves a declared dependency's output with its exact type.
func (in Inputs) Get[T any](output Output[T]) (T, bool) {
	var zero T
	if output.graph == nil || output.graph != in.graph {
		return zero, false
	}
	if _, ok := in.allowed[output.name]; !ok {
		return zero, false
	}
	boxed, ok := in.values[output.name].(outputValue[T])
	if !ok {
		return zero, false
	}
	return boxed.value, true
}

// dependency is one After declaration before compilation.
type dependency struct {
	graph *graphID
	name  string
	when  Condition
}

// step is one registration before compilation.
type step struct {
	name    string
	run     func(context.Context, Inputs) (any, error)
	deps    []dependency
	timeout time.Duration
	retry   cron.RetryPolicy
}

// StepOption configures one step at Builder.Step or Builder.Job time. Options
// are applied in order; failures are reported by Build.
type StepOption func(*step) error

// After declares that the step runs only after output's step completes with a
// result matching when. A step waits for all its dependencies and is skipped
// if any condition fails. The zero Output or an unknown Condition fails Build
// with ErrInvalidOption; declaring the same dependency twice fails it with
// ErrDuplicateDep.
func After[T any](output Output[T], when Condition) StepOption {
	return func(s *step) error {
		if output.graph == nil || output.name == "" {
			return fmt.Errorf("%w: dependency is empty", ErrInvalidOption)
		}
		if !when.valid() {
			return fmt.Errorf("%w: unknown condition %d", ErrInvalidOption, when)
		}
		s.deps = append(s.deps, dependency{graph: output.graph, name: output.name, when: when})
		return nil
	}
}

// WithTimeout caps one step run; the step context is cancelled with
// cron.ErrJobTimeout as the cause. Zero disables the timeout; a negative
// timeout fails Build with ErrInvalidOption.
func WithTimeout(timeout time.Duration) StepOption {
	return func(s *step) error {
		if timeout < 0 {
			return fmt.Errorf("%w: timeout must not be negative", ErrInvalidOption)
		}
		s.timeout = timeout
		return nil
	}
}

// WithRetry re-runs one step on error according to policy (see
// cron.RetryPolicy). A policy with negative delays or a jitter fraction
// outside [0, 1] fails Build with ErrInvalidOption.
func WithRetry(policy cron.RetryPolicy) StepOption {
	return func(s *step) error {
		invalidDelay := policy.Initial < 0 || policy.MaxDelay < 0 || policy.Multiplier < 0
		invalidJitter := policy.JitterFrac < 0 || policy.JitterFrac > 1
		if invalidDelay || invalidJitter {
			return fmt.Errorf("%w: invalid retry policy", ErrInvalidOption)
		}
		s.retry = policy
		return nil
	}
}

// builderConfig is the Builder configuration assembled by New.
type builderConfig struct {
	maxParallelism int
}

// Option configures a Builder; see New.
type Option func(*builderConfig) error

// WithMaxParallelism limits how many steps run at the same time. The default
// is 32; a non-positive limit fails Build with ErrInvalidOption.
func WithMaxParallelism(limit int) Option {
	return func(config *builderConfig) error {
		if limit <= 0 {
			return fmt.Errorf("%w: max parallelism must be positive", ErrInvalidOption)
		}
		config.maxParallelism = limit
		return nil
	}
}

// Builder incrementally constructs one Workflow. Errors from options and
// steps are collected and reported by Build, so a sequence of Step calls
// needs no error handling. Build freezes the Builder: later Step and Job
// calls are ignored and make every subsequent Build return ErrBuilderFrozen.
// Builder is not safe for concurrent use; its zero value is ready to use.
type Builder struct {
	graph      *graphID
	config     builderConfig
	steps      []step
	errors     []error
	frozen     bool
	afterBuild error
	built      *Workflow
	buildErr   error
}

// New returns an empty Builder configured by opts. Invalid options are
// reported by Build, not here.
func New(opts ...Option) *Builder {
	builder := &Builder{}
	builder.init()
	for i, option := range opts {
		if option == nil {
			builder.errors = append(builder.errors,
				fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i))
			continue
		}
		if err := option(&builder.config); err != nil {
			builder.errors = append(builder.errors, err)
		}
	}
	return builder
}

// Step adds a step computing a T and returns its Output for use with After
// and Get. Steps run in dependency order, not registration order. name must
// be unique, non-empty and free of surrounding whitespace (Build reports
// ErrDuplicateStep or ErrInvalidName); a nil fn is reported as ErrNilJob. A
// panic in fn is recovered into a failure result.
func (b *Builder) Step[T any](
	name string,
	fn func(context.Context, Inputs) (T, error),
	opts ...StepOption,
) Output[T] {
	if !b.canMutate() {
		return Output[T]{}
	}
	output := Output[T]{graph: b.graph, name: name}
	s := step{name: name}
	if fn == nil {
		b.errors = append(b.errors, &ConfigError{Err: ErrNilJob, Step: name})
	} else {
		s.run = func(ctx context.Context, inputs Inputs) (any, error) {
			value, err := fn(ctx, inputs)
			if err != nil {
				return nil, err
			}
			return outputValue[T]{value: value}, nil
		}
	}
	b.add(s, opts)
	return output
}

// Job adds a cron.Job step whose Output type is Unit. It follows the same
// rules as Step.
func (b *Builder) Job(name string, job cron.Job, opts ...StepOption) Output[Unit] {
	if !b.canMutate() {
		return Output[Unit]{}
	}
	output := Output[Unit]{graph: b.graph, name: name}
	s := step{name: name}
	if job == nil || isNilLike(job) {
		b.errors = append(b.errors, &ConfigError{Err: ErrNilJob, Step: name})
	} else {
		s.run = func(ctx context.Context, _ Inputs) (any, error) {
			if err := job.Run(ctx); err != nil {
				return nil, err
			}
			return outputValue[Unit]{value: Unit{}}, nil
		}
	}
	b.add(s, opts)
	return output
}

// Build validates the graph, freezes the Builder and returns an immutable
// Workflow. Repeated calls return the same result. It returns the joined
// option errors (wrapping ErrInvalidOption), a *ConfigError wrapping
// ErrNilJob, ErrInvalidName, ErrDuplicateStep, ErrUnknownDep or
// ErrDuplicateDep, ErrCycle with the cycle's path, or ErrBuilderFrozen when
// steps were added after an earlier Build.
func (b *Builder) Build() (*Workflow, error) {
	if b == nil {
		return nil, errors.New("workflow: nil builder")
	}
	b.init()
	if b.afterBuild != nil {
		return nil, b.afterBuild
	}
	if b.frozen {
		return b.built, b.buildErr
	}
	b.frozen = true
	b.built, b.buildErr = b.compile()
	return b.built, b.buildErr
}

// MustBuild is Build that panics on error, for graphs fixed at build time.
func (b *Builder) MustBuild() *Workflow {
	workflow, err := b.Build()
	if err != nil {
		panic(err)
	}
	return workflow
}

// init lazily prepares a zero-value Builder.
func (b *Builder) init() {
	if b.graph == nil {
		b.graph = &graphID{}
	}
	if b.config.maxParallelism == 0 {
		b.config.maxParallelism = defaultMaxParallelism
	}
}

// canMutate reports whether steps may still be added; a frozen Builder
// records ErrBuilderFrozen for the next Build.
func (b *Builder) canMutate() bool {
	b.init()
	if !b.frozen {
		return true
	}
	b.afterBuild = ErrBuilderFrozen
	return false
}

// add applies opts to s and appends it; option failures are deferred to
// Build.
func (b *Builder) add(s step, opts []StepOption) {
	for i, option := range opts {
		if option == nil {
			b.errors = append(b.errors,
				fmt.Errorf("%w: step %q option %d is nil", ErrInvalidOption, s.name, i))
			continue
		}
		if err := option(&s); err != nil {
			b.errors = append(b.errors, fmt.Errorf("step %q: %w", s.name, err))
		}
	}
	b.steps = append(b.steps, s)
}

// compile validates names and dependencies, resolves them to indexes and
// rejects cycles.
func (b *Builder) compile() (*Workflow, error) {
	if err := errors.Join(b.errors...); err != nil {
		return nil, err
	}

	steps := make([]compiledStep, len(b.steps))
	index := make(map[string]int, len(b.steps))
	for i, source := range b.steps {
		if source.name == "" || strings.TrimSpace(source.name) != source.name {
			return nil, &ConfigError{Err: ErrInvalidName, Step: source.name}
		}
		if _, exists := index[source.name]; exists {
			return nil, &ConfigError{Err: ErrDuplicateStep, Step: source.name}
		}
		index[source.name] = i
		steps[i] = compiledStep{
			name:    source.name,
			run:     source.run,
			timeout: source.timeout,
			retry:   source.retry,
			deps:    make([]compiledDependency, 0, len(source.deps)),
		}
	}

	for i, source := range b.steps {
		seen := make(map[int]struct{}, len(source.deps))
		for _, dep := range source.deps {
			depIndex, exists := index[dep.name]
			if dep.graph != b.graph || !exists {
				return nil, &ConfigError{Err: ErrUnknownDep, Step: source.name, Dep: dep.name}
			}
			if _, duplicate := seen[depIndex]; duplicate {
				return nil, &ConfigError{Err: ErrDuplicateDep, Step: source.name, Dep: dep.name}
			}
			seen[depIndex] = struct{}{}
			steps[i].deps = append(steps[i].deps, compiledDependency{
				step: depIndex,
				when: dep.when,
			})
			steps[depIndex].dependents = append(steps[depIndex].dependents, i)
		}
	}
	if cycle := findCycle(steps); len(cycle) != 0 {
		return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(cycle, " -> "))
	}
	return &Workflow{
		graph:          b.graph,
		steps:          steps,
		maxParallelism: b.config.maxParallelism,
	}, nil
}

// compiledDependency is a dependency resolved to a step index.
type compiledDependency struct {
	step int
	when Condition
}

// compiledStep is a step with its dependencies and dependents resolved to
// indexes.
type compiledStep struct {
	name       string
	run        func(context.Context, Inputs) (any, error)
	deps       []compiledDependency
	dependents []int
	timeout    time.Duration
	retry      cron.RetryPolicy
}

// Workflow is an immutable DAG produced by Builder.Build. It implements
// cron.Job, so it can be registered with Cron.Add or Cron.AddSchedule, and is
// safe for concurrent use: every Run or Execute is an independent execution.
type Workflow struct {
	graph          *graphID
	steps          []compiledStep
	maxParallelism int
	onComplete     func(*Execution)
}

// WithOnComplete returns a copy of w that calls cb with the finished
// Execution, on the calling goroutine, before Execute returns. w itself is
// unchanged.
func (w *Workflow) WithOnComplete(cb func(*Execution)) *Workflow {
	copy := *w
	copy.onComplete = cb
	return &copy
}

// Run executes the DAG once and returns Execution.Err, which makes a Workflow
// a cron.Job.
func (w *Workflow) Run(ctx context.Context) error { return w.Execute(ctx).Err() }

// Execute runs the DAG once and blocks until every step has completed or been
// skipped; at most the configured number of steps run at the same time. A
// step is skipped when a dependency condition is not met or when ctx is
// already cancelled by the time the step becomes ready; steps already running
// observe the cancellation through their context. A nil ctx skips every step
// and reports ErrNilContext.
func (w *Workflow) Execute(ctx context.Context) *Execution {
	var invocationErr error
	if ctx == nil {
		invocationErr = ErrNilContext
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(context.Background())
		cancel(ErrNilContext)
	}

	id := uuid.NewV7()
	begin := time.Now()
	states := make([]stepState, len(w.steps))
	remainingDeps := make([]int, len(w.steps))
	ready := make([]int, 0, len(w.steps))
	for i, step := range w.steps {
		remainingDeps[i] = len(step.deps)
		if len(step.deps) == 0 {
			ready = append(ready, i)
		}
	}

	done := make(chan stepDone, min(w.maxParallelism, max(1, len(w.steps))))
	remaining := len(w.steps)
	var running int
	finish := func(completed stepDone) {
		states[completed.index] = stepState{
			result:    completed.result,
			err:       completed.err,
			output:    completed.output,
			startedAt: completed.startedAt,
			duration:  completed.duration,
		}
		remaining--
		for _, dependent := range w.steps[completed.index].dependents {
			remainingDeps[dependent]--
			if remainingDeps[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}

	for remaining > 0 {
		for len(ready) > 0 && running < w.maxParallelism {
			index := ready[0]
			ready = ready[1:]
			if err := context.Cause(ctx); err != nil {
				finish(stepDone{index: index, result: ResultSkipped, err: err})
				continue
			}
			if !dependenciesMatch(w.steps[index], states) {
				finish(stepDone{index: index, result: ResultSkipped})
				continue
			}
			inputs := buildInputs(
				w.graph,
				w.steps[index],
				states,
				w.steps,
			)
			running++
			go func() {
				done <- runStep(
					ctx,
					index,
					w.steps[index],
					inputs,
				)
			}()
		}
		if remaining == 0 {
			break
		}
		if running == 0 {
			panic("workflow: validated graph made no progress")
		}
		completed := <-done
		running--
		finish(completed)
	}

	execution := &Execution{
		ID:            id,
		StartedAt:     begin,
		Duration:      time.Since(begin),
		graph:         w.graph,
		order:         make([]string, len(w.steps)),
		steps:         make(map[string]StepReport, len(w.steps)),
		invocationErr: invocationErr,
	}
	for i, step := range w.steps {
		execution.order[i] = step.name
		state := states[i]
		execution.steps[step.name] = StepReport{
			Result:    state.result,
			Err:       state.err,
			StartedAt: state.startedAt,
			Duration:  state.duration,
			output:    state.output,
		}
	}
	if w.onComplete != nil {
		w.onComplete(execution)
	}
	return execution
}

// StepReport is one step's outcome within an Execution.
type StepReport struct {
	Result    Result
	Err       error         // step error, or the cancellation cause for a step skipped by a cancelled context
	StartedAt time.Time     // zero for skipped steps
	Duration  time.Duration // zero for skipped steps
	output    any
}

// Execution reports one completed Workflow run. It is immutable and safe for
// concurrent reads; the accessors return copies.
type Execution struct {
	ID            uuid.UUID // time-ordered (version 7), unique per run
	StartedAt     time.Time
	Duration      time.Duration
	graph         *graphID
	order         []string
	steps         map[string]StepReport
	invocationErr error
}

// Result returns the named step's Result; ok is false for an unknown name.
func (e *Execution) Result(name string) (Result, bool) {
	report, ok := e.steps[name]
	return report.Result, ok
}

// Error returns the named step's error, or nil for success, a skip without
// cause, or an unknown name.
func (e *Execution) Error(name string) error { return e.steps[name].Err }

// Step returns the named step's report; ok is false for an unknown name.
func (e *Execution) Step(name string) (StepReport, bool) {
	report, ok := e.steps[name]
	return report, ok
}

// Results returns a copy of every step's Result keyed by name.
func (e *Execution) Results() map[string]Result {
	results := make(map[string]Result, len(e.steps))
	for name, report := range e.steps {
		results[name] = report.Result
	}
	return results
}

// Errors returns a copy of the non-nil step errors keyed by name.
func (e *Execution) Errors() map[string]error {
	errs := make(map[string]error)
	for name, report := range e.steps {
		if report.Err != nil {
			errs[name] = report.Err
		}
	}
	return errs
}

// Steps returns a copy of every step's report keyed by name.
func (e *Execution) Steps() map[string]StepReport { return maps.Clone(e.steps) }

// Get returns the typed output of a successful step. ok is false when the
// step failed or was skipped, when output belongs to another graph, or for
// the zero Output.
func (e *Execution) Get[T any](output Output[T]) (T, bool) {
	var zero T
	if output.graph == nil || output.graph != e.graph {
		return zero, false
	}
	report, ok := e.steps[output.name]
	if !ok || report.Result != ResultSuccess {
		return zero, false
	}
	boxed, ok := report.output.(outputValue[T])
	if !ok {
		return zero, false
	}
	return boxed.value, true
}

// Err joins the step errors in registration order, or returns nil when no
// step failed. A step skipped because the context was cancelled contributes
// the cancellation cause; one skipped for an unmet condition contributes
// nothing. A nil ctx passed to Execute yields ErrNilContext.
func (e *Execution) Err() error {
	errs := make([]error, 0, len(e.steps)+1)
	for _, name := range e.order {
		if err := e.steps[name].Err; err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 && e.invocationErr != nil {
		errs = append(errs, e.invocationErr)
	}
	return errors.Join(errs...)
}

// stepState is the executor's record of one step's outcome.
type stepState struct {
	result    Result
	err       error
	output    any
	startedAt time.Time
	duration  time.Duration
}

// stepDone is the completion message sent by a step goroutine.
type stepDone struct {
	index     int
	result    Result
	err       error
	output    any
	startedAt time.Time
	duration  time.Duration
}

// dependenciesMatch reports whether every dependency condition of step holds.
func dependenciesMatch(step compiledStep, states []stepState) bool {
	for _, dep := range step.deps {
		if !dep.when.match(states[dep.step].result) {
			return false
		}
	}
	return true
}

// buildInputs collects the successful outputs of step's dependencies and
// records which names the step may read.
func buildInputs(graph *graphID, step compiledStep, states []stepState, steps []compiledStep) Inputs {
	inputs := Inputs{
		graph:   graph,
		allowed: make(map[string]struct{}, len(step.deps)),
		values:  make(map[string]any, len(step.deps)),
	}
	for _, dep := range step.deps {
		name := steps[dep.step].name
		inputs.allowed[name] = struct{}{}
		if states[dep.step].result == ResultSuccess {
			inputs.values[name] = states[dep.step].output
		}
	}
	return inputs
}

// runStep executes one step with its timeout and retry policy, converting a
// panic into a failure.
func runStep(ctx context.Context, index int, step compiledStep, inputs Inputs) (completed stepDone) {
	completed.index = index
	completed.result = ResultFailure
	completed.startedAt = time.Now()
	defer func() {
		completed.duration = time.Since(completed.startedAt)
		if recovered := recover(); recovered != nil {
			completed.err = fmt.Errorf("workflow: step %q panicked: %v", step.name, recovered)
		}
	}()

	runCtx := ctx
	if step.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(ctx, step.timeout, cron.ErrJobTimeout)
		defer cancel()
	}
	var output any
	var job cron.Job = cron.JobFunc(func(stepCtx context.Context) error {
		value, err := step.run(stepCtx, inputs)
		if err != nil {
			return err
		}
		output = value
		return nil
	})
	if !step.retry.IsZero() {
		job = step.retry.Wrapper()(job)
	}
	if err := job.Run(runCtx); err != nil {
		completed.err = err
		return completed
	}
	completed.result = ResultSuccess
	completed.output = output
	return completed
}

// findCycle returns the names along the first dependency cycle found by a
// depth-first search, or nil for an acyclic graph.
func findCycle(steps []compiledStep) []string {
	colors := make([]uint8, len(steps))
	positions := make([]int, len(steps))
	for i := range positions {
		positions[i] = -1
	}
	stack := make([]int, 0, len(steps))
	var cycle []string
	var visit func(int) bool
	visit = func(index int) bool {
		colors[index] = 1
		positions[index] = len(stack)
		stack = append(stack, index)
		for _, dep := range steps[index].deps {
			switch colors[dep.step] {
			case 0:
				if visit(dep.step) {
					return true
				}
			case 1:
				for _, member := range stack[positions[dep.step]:] {
					cycle = append(cycle, steps[member].name)
				}
				cycle = append(cycle, steps[dep.step].name)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		positions[index] = -1
		colors[index] = 2
		return false
	}
	for i := range steps {
		if colors[i] == 0 && visit(i) {
			return cycle
		}
	}
	return nil
}

// isNilLike reports whether an interface value holds a nil pointer, func,
// map, slice or channel, so a typed nil cron.Job is rejected at Build.
func isNilLike(value any) bool {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
