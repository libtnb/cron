// Package workflow builds typed DAGs of cron jobs.
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

// Errors reported while configuring or executing a workflow.
var (
	ErrDuplicateStep = errors.New("workflow: duplicate step")
	ErrDuplicateDep  = errors.New("workflow: duplicate dependency")
	ErrUnknownDep    = errors.New("workflow: unknown dependency")
	ErrCycle         = errors.New("workflow: dependency cycle")
	ErrNilJob        = errors.New("workflow: step has no job")
	ErrInvalidName   = errors.New("workflow: invalid step name")
	ErrInvalidOption = errors.New("workflow: invalid option")
	ErrBuilderFrozen = errors.New("workflow: builder is frozen")
	ErrNilContext    = errors.New("workflow: nil context")
)

// ConfigError identifies the step involved in an invalid graph.
type ConfigError struct {
	Err  error
	Step string
	Dep  string
}

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

func (e *ConfigError) Unwrap() error { return e.Err }

// Result is a step outcome.
type Result uint8

const (
	ResultPending Result = iota
	ResultSuccess
	ResultFailure
	ResultSkipped
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

// Condition selects the upstream result required by After.
type Condition uint8

const (
	conditionUnknown Condition = iota
	OnSuccess
	OnFailure
	OnSkipped
	OnComplete
)

// Unit is the output of a plain cron.Job step.
type Unit struct{}

type graphID struct{}

// Output identifies one typed step output.
type Output[T any] struct {
	graph *graphID
	name  string
}

// Name returns the step name.
func (o Output[T]) Name() string { return o.name }

type outputValue[T any] struct{ value T }

// Inputs exposes the successful outputs of a step's declared dependencies.
type Inputs struct {
	graph   *graphID
	allowed map[string]struct{}
	values  map[string]any
}

// Get resolves a declared dependency with the exact output type.
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

type dependency struct {
	graph *graphID
	name  string
	when  Condition
}

type step struct {
	name    string
	run     func(context.Context, Inputs) (any, error)
	deps    []dependency
	timeout time.Duration
	retry   cron.RetryPolicy
}

// StepOption configures one step.
type StepOption func(*step) error

// After declares an upstream dependency and its required result.
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

// WithTimeout caps one step run. Zero disables the timeout.
func WithTimeout(timeout time.Duration) StepOption {
	return func(s *step) error {
		if timeout < 0 {
			return fmt.Errorf("%w: timeout must not be negative", ErrInvalidOption)
		}
		s.timeout = timeout
		return nil
	}
}

// WithRetry applies a retry policy to one step.
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

const defaultMaxParallelism = 32

type builderConfig struct {
	maxParallelism int
}

// Option configures a Builder.
type Option func(*builderConfig) error

// WithMaxParallelism limits simultaneously running steps. The default is 32.
func WithMaxParallelism(limit int) Option {
	return func(config *builderConfig) error {
		if limit <= 0 {
			return fmt.Errorf("%w: max parallelism must be positive", ErrInvalidOption)
		}
		config.maxParallelism = limit
		return nil
	}
}

// Builder incrementally constructs one Workflow. Build freezes it. Builder is
// not safe for concurrent use; its zero value is ready to use.
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

// New returns an empty Builder. Invalid options are reported by Build.
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

// Step adds a typed function step. Configuration errors are deferred to Build.
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

// Job adds a cron.Job step. Configuration errors are deferred to Build.
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

// Build validates the graph, freezes the Builder, and returns an immutable
// Workflow. Repeated calls return the same result.
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

// MustBuild is Build with panic-on-error semantics.
func (b *Builder) MustBuild() *Workflow {
	workflow, err := b.Build()
	if err != nil {
		panic(err)
	}
	return workflow
}

type compiledDependency struct {
	step int
	when Condition
}

type compiledStep struct {
	name       string
	run        func(context.Context, Inputs) (any, error)
	deps       []compiledDependency
	dependents []int
	timeout    time.Duration
	retry      cron.RetryPolicy
}

// Workflow is an immutable DAG and implements cron.Job.
type Workflow struct {
	graph          *graphID
	steps          []compiledStep
	maxParallelism int
	onComplete     func(*Execution)
}

// WithOnComplete returns a copy that calls cb before Execute returns.
func (w *Workflow) WithOnComplete(cb func(*Execution)) *Workflow {
	copy := *w
	copy.onComplete = cb
	return &copy
}

// Run executes the DAG once and returns its joined error.
func (w *Workflow) Run(ctx context.Context) error { return w.Execute(ctx).Err() }

// Execute runs the DAG once. At most the configured number of steps run at
// the same time.
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

// StepReport contains one step's outcome.
type StepReport struct {
	Result    Result
	Err       error
	StartedAt time.Time
	Duration  time.Duration
	output    any
}

// Execution reports one completed Workflow run.
type Execution struct {
	ID            uuid.UUID
	StartedAt     time.Time
	Duration      time.Duration
	graph         *graphID
	order         []string
	steps         map[string]StepReport
	invocationErr error
}

// Result returns the named result.
func (e *Execution) Result(name string) (Result, bool) {
	report, ok := e.steps[name]
	return report.Result, ok
}

// Error returns the named step error.
func (e *Execution) Error(name string) error { return e.steps[name].Err }

// Step returns the named report.
func (e *Execution) Step(name string) (StepReport, bool) {
	report, ok := e.steps[name]
	return report, ok
}

// Results returns a copy of all results.
func (e *Execution) Results() map[string]Result {
	results := make(map[string]Result, len(e.steps))
	for name, report := range e.steps {
		results[name] = report.Result
	}
	return results
}

// Errors returns a copy of non-nil step errors.
func (e *Execution) Errors() map[string]error {
	errs := make(map[string]error)
	for name, report := range e.steps {
		if report.Err != nil {
			errs[name] = report.Err
		}
	}
	return errs
}

// Steps returns a copy of all reports.
func (e *Execution) Steps() map[string]StepReport { return maps.Clone(e.steps) }

// Get resolves a successful output from this execution.
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

// Err joins step errors in graph order.
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

func (c Condition) valid() bool { return c >= OnSuccess && c <= OnComplete }

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

func (b *Builder) init() {
	if b.graph == nil {
		b.graph = &graphID{}
	}
	if b.config.maxParallelism == 0 {
		b.config.maxParallelism = defaultMaxParallelism
	}
}

func (b *Builder) canMutate() bool {
	b.init()
	if !b.frozen {
		return true
	}
	b.afterBuild = ErrBuilderFrozen
	return false
}

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

type stepState struct {
	result    Result
	err       error
	output    any
	startedAt time.Time
	duration  time.Duration
}

type stepDone struct {
	index     int
	result    Result
	err       error
	output    any
	startedAt time.Time
	duration  time.Duration
}

func dependenciesMatch(step compiledStep, states []stepState) bool {
	for _, dep := range step.deps {
		if !dep.when.match(states[dep.step].result) {
			return false
		}
	}
	return true
}

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

func isNilLike(value any) bool {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

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
