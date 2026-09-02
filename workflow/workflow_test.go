package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/workflow"
)

func TestWorkflow_LinearChainSuccess(t *testing.T) {
	var order []string
	job := func(name string) cron.Job {
		return cron.JobFunc(func(context.Context) error {
			order = append(order, name)
			return nil
		})
	}
	builder := workflow.New()
	a := builder.Job("A", job("A"))
	b := builder.Job("B", job("B"), workflow.After(a, workflow.OnSuccess))
	builder.Job("C", job("C"), workflow.After(b, workflow.OnSuccess))

	if err := builder.MustBuild().Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ""); got != "ABC" {
		t.Fatalf("order = %q, want ABC", got)
	}
}

func TestWorkflow_Conditions(t *testing.T) {
	boom := errors.New("boom")
	var recovered atomic.Bool
	var skipped atomic.Bool
	var completed atomic.Bool
	builder := workflow.New()
	fail := builder.Job("fail", cron.JobFunc(func(context.Context) error { return boom }))
	blocked := builder.Job("blocked", cron.JobFunc(func(context.Context) error {
		t.Fatal("blocked step ran")
		return nil
	}), workflow.After(fail, workflow.OnSuccess))
	builder.Job("recover", cron.JobFunc(func(context.Context) error {
		recovered.Store(true)
		return nil
	}), workflow.After(fail, workflow.OnFailure))
	builder.Job("skip-handler", cron.JobFunc(func(context.Context) error {
		skipped.Store(true)
		return nil
	}), workflow.After(blocked, workflow.OnSkipped))
	builder.Job("complete", cron.JobFunc(func(context.Context) error {
		completed.Store(true)
		return nil
	}), workflow.After(fail, workflow.OnComplete))

	err := builder.MustBuild().Run(t.Context())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if !recovered.Load() || !skipped.Load() || !completed.Load() {
		t.Fatalf("conditions: recovered=%v skipped=%v completed=%v",
			recovered.Load(), skipped.Load(), completed.Load())
	}
}

func TestWorkflow_TypedDataFlow(t *testing.T) {
	builder := workflow.New()
	a := builder.Step[int]("a", func(context.Context, workflow.Inputs) (int, error) { return 2, nil })
	b := builder.Step[int]("b", func(context.Context, workflow.Inputs) (int, error) { return 3, nil })
	sum := builder.Step[int]("sum", func(_ context.Context, inputs workflow.Inputs) (int, error) {
		left, ok := inputs.Get(a)
		if !ok {
			return 0, errors.New("missing a")
		}
		right, ok := inputs.Get(b)
		if !ok {
			return 0, errors.New("missing b")
		}
		return left + right, nil
	}, workflow.After(a, workflow.OnSuccess), workflow.After(b, workflow.OnSuccess))

	execution := builder.MustBuild().Execute(t.Context())
	got, ok := execution.Get(sum)
	if !ok || got != 5 {
		t.Fatalf("sum = %d, %v; want 5, true", got, ok)
	}
	if a.Name() != "a" {
		t.Fatalf("output name = %q", a.Name())
	}
	if execution.ID == uuid.Nil() || execution.StartedAt.IsZero() || execution.Duration < 0 {
		t.Fatalf("execution metadata = %+v", execution)
	}
	if _, ok := execution.Get(workflow.Output[string]{}); ok {
		t.Fatal("empty output unexpectedly resolved")
	}
}

func TestInputs_RejectsUndeclaredAndForeignOutputs(t *testing.T) {
	foreignBuilder := workflow.New()
	foreign := foreignBuilder.Step[int]("foreign", func(context.Context, workflow.Inputs) (int, error) {
		return 1, nil
	})

	builder := workflow.New()
	root := builder.Step[int]("root", func(context.Context, workflow.Inputs) (int, error) { return 2, nil })
	builder.Step[int]("child", func(_ context.Context, inputs workflow.Inputs) (int, error) {
		if _, ok := inputs.Get(foreign); ok {
			t.Fatal("foreign output resolved")
		}
		value, ok := inputs.Get(root)
		if !ok {
			return 0, errors.New("root output missing")
		}
		return value, nil
	}, workflow.After(root, workflow.OnSuccess))
	if err := builder.MustBuild().Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflow_FailedOutputIsDiscarded(t *testing.T) {
	boom := errors.New("boom")
	builder := workflow.New()
	fail := builder.Step[string]("fail", func(context.Context, workflow.Inputs) (string, error) {
		return "partial", boom
	})
	execution := builder.MustBuild().Execute(t.Context())
	if _, ok := execution.Get(fail); ok {
		t.Fatal("failed step retained its output")
	}
	if got := execution.Errors(); !errors.Is(got["fail"], boom) {
		t.Fatalf("Errors = %v", got)
	}
	if !errors.Is(execution.Err(), boom) || !errors.Is(execution.Error("fail"), boom) {
		t.Fatalf("execution error = %v", execution.Err())
	}
}

func TestWorkflow_PanicBecomesFailure(t *testing.T) {
	builder := workflow.New()
	builder.Job("boom", cron.JobFunc(func(context.Context) error { panic("detail") }))
	err := builder.MustBuild().Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `step "boom" panicked: detail`) {
		t.Fatalf("panic error = %v", err)
	}
}

func TestWorkflow_ErrJoinsFailuresInGraphOrder(t *testing.T) {
	errA := errors.New("a")
	errB := errors.New("b")
	builder := workflow.New()
	builder.Job("a", cron.JobFunc(func(context.Context) error { return errA }))
	builder.Job("b", cron.JobFunc(func(context.Context) error { return errB }))
	err := builder.MustBuild().Run(t.Context())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("joined error = %v", err)
	}
}

func TestWorkflow_OnCompleteAndImmutableReports(t *testing.T) {
	builder := workflow.New()
	builder.Job("ok", cron.JobFunc(func(context.Context) error { return nil }))
	original := builder.MustBuild()
	var calls atomic.Int32
	withCallback := original.WithOnComplete(func(execution *workflow.Execution) {
		calls.Add(1)
		result, ok := execution.Result("ok")
		if !ok || result != workflow.ResultSuccess {
			t.Errorf("result = %v, %v", result, ok)
		}
	})
	if err := original.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("WithOnComplete mutated original workflow")
	}
	execution := withCallback.Execute(t.Context())
	if calls.Load() != 1 {
		t.Fatalf("callback calls = %d", calls.Load())
	}

	results := execution.Results()
	results["ok"] = workflow.ResultFailure
	if result, _ := execution.Result("ok"); result != workflow.ResultSuccess {
		t.Fatal("Results exposed mutable internal state")
	}
	errs := execution.Errors()
	errs["ok"] = errors.New("injected")
	if execution.Error("ok") != nil {
		t.Fatal("Errors exposed mutable internal state")
	}
	steps := execution.Steps()
	steps["ok"] = workflow.StepReport{Result: workflow.ResultFailure}
	report, ok := execution.Step("ok")
	if !ok || report.Result != workflow.ResultSuccess || report.StartedAt.IsZero() {
		t.Fatalf("step report = %+v, %v", report, ok)
	}
	if _, ok := execution.Step("missing"); ok || execution.Error("missing") != nil {
		t.Fatal("missing step unexpectedly found")
	}
}

func TestWorkflow_ContextCancellationReleasesWaiters(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	builder := workflow.New()
	block := builder.Job("block", cron.JobFunc(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	builder.Job("after", cron.JobFunc(func(context.Context) error {
		t.Fatal("after ran")
		return nil
	}), workflow.After(block, workflow.OnSuccess))
	done := make(chan error, 1)
	go func() { done <- builder.MustBuild().Run(ctx) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkflow_PreCancelledContextSkipsAllSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var ran atomic.Bool
	builder := workflow.New()
	builder.Job("root", cron.JobFunc(func(context.Context) error {
		ran.Store(true)
		return nil
	}))
	execution := builder.MustBuild().Execute(ctx)
	if ran.Load() {
		t.Fatal("step ran with pre-cancelled context")
	}
	result, _ := execution.Result("root")
	if result != workflow.ResultSkipped || !errors.Is(execution.Err(), context.Canceled) {
		t.Fatalf("result=%v error=%v", result, execution.Err())
	}
}

func TestWorkflow_NilContextIsRejected(t *testing.T) {
	builder := workflow.New()
	builder.Job("root", cron.JobFunc(func(context.Context) error { return nil }))
	execution := builder.MustBuild().Execute(nil) //nolint:staticcheck // a nil Context is the input under test
	if !errors.Is(execution.Err(), workflow.ErrNilContext) {
		t.Fatalf("Execute(nil) error = %v", execution.Err())
	}
	if result, _ := execution.Result("root"); result != workflow.ResultSkipped {
		t.Fatalf("root result = %v, want skipped", result)
	}
}

func TestWorkflow_PreservesSuccessfulNilOutput(t *testing.T) {
	builder := workflow.New()
	output := builder.Step[any]("nil", func(context.Context, workflow.Inputs) (any, error) {
		return nil, nil
	})
	execution := builder.MustBuild().Execute(t.Context())
	value, ok := execution.Get(output)
	if !ok || value != nil {
		t.Fatalf("Get = %#v, %v; want nil, true", value, ok)
	}
}

func TestWorkflow_MaxParallelism(t *testing.T) {
	const limit = 2
	var active atomic.Int32
	var peak atomic.Int32
	release := make(chan struct{})
	builder := workflow.New(workflow.WithMaxParallelism(limit))
	for i := range 8 {
		builder.Job(fmt.Sprintf("step-%d", i), cron.JobFunc(func(context.Context) error {
			current := active.Add(1)
			for {
				previous := peak.Load()
				if current <= previous || peak.CompareAndSwap(previous, current) {
					break
				}
			}
			<-release
			active.Add(-1)
			return nil
		}))
	}
	done := make(chan error, 1)
	go func() { done <- builder.MustBuild().Run(t.Context()) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && peak.Load() < limit {
		time.Sleep(time.Millisecond)
	}
	if got := peak.Load(); got != limit {
		close(release)
		<-done
		t.Fatalf("peak parallelism = %d, want %d", got, limit)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("peak parallelism = %d, limit %d", got, limit)
	}
}

func TestWorkflow_CancellationPreservesCause(t *testing.T) {
	cause := errors.New("server stopping")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	builder := workflow.New()
	builder.Job("root", cron.JobFunc(func(context.Context) error { return nil }))
	if err := builder.MustBuild().Run(ctx); !errors.Is(err, cause) {
		t.Fatalf("Run error = %v, want %v", err, cause)
	}
}

func TestWorkflow_WaitsForEveryDependencyBeforeSkipping(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Bool
	var observedRunning atomic.Bool
	builder := workflow.New()
	fail := builder.Job("fail", cron.JobFunc(func(context.Context) error { return errors.New("fail") }))
	block := builder.Job("block", cron.JobFunc(func(context.Context) error {
		running.Store(true)
		<-release
		running.Store(false)
		return nil
	}))
	skipped := builder.Job("skipped", cron.JobFunc(func(context.Context) error {
		t.Fatal("skipped step ran")
		return nil
	}), workflow.After(fail, workflow.OnSuccess), workflow.After(block, workflow.OnSuccess))
	builder.Job("downstream", cron.JobFunc(func(context.Context) error {
		observedRunning.Store(running.Load())
		return nil
	}), workflow.After(skipped, workflow.OnSkipped))

	done := make(chan error, 1)
	go func() { done <- builder.MustBuild().Run(t.Context()) }()
	for !running.Load() {
		runtime.Gosched()
	}
	close(release)
	<-done
	if observedRunning.Load() {
		t.Fatal("downstream ran before dependency barrier completed")
	}
}

func TestWorkflow_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		builder := workflow.New()
		builder.Job("slow", cron.JobFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return context.Cause(ctx)
		}), workflow.WithTimeout(time.Hour))
		execution := builder.MustBuild().Execute(t.Context())
		if !errors.Is(execution.Err(), cron.ErrJobTimeout) {
			t.Fatalf("error = %v", execution.Err())
		}
		report, _ := execution.Step("slow")
		if report.Duration != time.Hour {
			t.Fatalf("duration = %v, want 1h", report.Duration)
		}
	})
}

func TestWorkflow_Retry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		builder := workflow.New()
		builder.Job("flaky", cron.JobFunc(func(context.Context) error {
			if attempts.Add(1) < 3 {
				return errors.New("flaky")
			}
			return nil
		}), workflow.WithRetry(cron.Retry(4, cron.RetryInitial(time.Second))))
		if err := builder.MustBuild().Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if attempts.Load() != 3 {
			t.Fatalf("attempts = %d", attempts.Load())
		}
	})
}

func TestBuilder_Validation(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		builder := workflow.New()
		builder.Job("same", cron.JobFunc(func(context.Context) error { return nil }))
		builder.Job("same", cron.JobFunc(func(context.Context) error { return nil }))
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrDuplicateStep) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		builder := workflow.New()
		builder.Job("", cron.JobFunc(func(context.Context) error { return nil }))
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrInvalidName) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nil job", func(t *testing.T) {
		builder := workflow.New()
		builder.Job("nil", nil)
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrNilJob) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("typed nil job", func(t *testing.T) {
		var job cron.JobFunc
		builder := workflow.New()
		builder.Job("nil", job)
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrNilJob) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nil function", func(t *testing.T) {
		builder := workflow.New()
		builder.Step[int]("nil", nil)
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrNilJob) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("foreign dependency", func(t *testing.T) {
		foreignBuilder := workflow.New()
		foreign := foreignBuilder.Job("foreign", cron.JobFunc(func(context.Context) error { return nil }))
		builder := workflow.New()
		builder.Job("local", cron.JobFunc(func(context.Context) error { return nil }),
			workflow.After(foreign, workflow.OnSuccess))
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrUnknownDep) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate dependency", func(t *testing.T) {
		builder := workflow.New()
		root := builder.Job("root", cron.JobFunc(func(context.Context) error { return nil }))
		builder.Job("child", cron.JobFunc(func(context.Context) error { return nil }),
			workflow.After(root, workflow.OnSuccess),
			workflow.After(root, workflow.OnComplete),
		)
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrDuplicateDep) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid options", func(t *testing.T) {
		builder := workflow.New()
		empty := workflow.Output[int]{}
		builder.Job("bad", cron.JobFunc(func(context.Context) error { return nil }),
			nil,
			workflow.After(empty, workflow.OnSuccess),
			workflow.WithTimeout(-time.Second),
			workflow.WithRetry(cron.RetryPolicy{JitterFrac: 2}),
		)
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrInvalidOption) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unknown condition", func(t *testing.T) {
		builder := workflow.New()
		root := builder.Job("root", cron.JobFunc(func(context.Context) error { return nil }))
		builder.Job("bad", cron.JobFunc(func(context.Context) error { return nil }),
			workflow.After(root, workflow.Condition(0)))
		_, err := builder.Build()
		if !errors.Is(err, workflow.ErrInvalidOption) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nil builder", func(t *testing.T) {
		var builder *workflow.Builder
		if _, err := builder.Build(); err == nil {
			t.Fatal("nil builder built")
		}
	})

	t.Run("invalid builder options", func(t *testing.T) {
		builder := workflow.New(nil, workflow.WithMaxParallelism(0))
		if _, err := builder.Build(); !errors.Is(err, workflow.ErrInvalidOption) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestBuilder_BuildFreezesGraph(t *testing.T) {
	builder := workflow.New()
	builder.Job("first", cron.JobFunc(func(context.Context) error { return nil }))
	first, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	again, err := builder.Build()
	if err != nil || again != first {
		t.Fatalf("repeated Build = %p, %v; want %p, nil", again, err, first)
	}
	if output := builder.Job("late", cron.JobFunc(func(context.Context) error { return nil })); output.Name() != "" {
		t.Fatalf("late output = %q, want invalid output", output.Name())
	}
	if _, err := builder.Build(); !errors.Is(err, workflow.ErrBuilderFrozen) {
		t.Fatalf("Build after mutation = %v", err)
	}
	if err := first.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBuilder_MustBuildPanics(t *testing.T) {
	builder := workflow.New()
	builder.Job("x", nil)
	defer func() {
		if recover() == nil {
			t.Fatal("MustBuild did not panic")
		}
	}()
	builder.MustBuild()
}

func TestConfigError_ErrorAndUnwrap(t *testing.T) {
	cases := []struct {
		err  *workflow.ConfigError
		want string
	}{
		{err: &workflow.ConfigError{Err: workflow.ErrDuplicateStep, Step: "a"}, want: `workflow: duplicate step: "a"`},
		{err: &workflow.ConfigError{Err: workflow.ErrNilJob, Step: "a"}, want: `workflow: step has no job: "a"`},
		{err: &workflow.ConfigError{Err: workflow.ErrInvalidName, Step: ""}, want: `workflow: invalid step name: ""`},
		{err: &workflow.ConfigError{Err: workflow.ErrUnknownDep, Step: "a", Dep: "b"}, want: `workflow: unknown dependency: step "a" depends on "b"`},
		{err: &workflow.ConfigError{Err: workflow.ErrDuplicateDep, Step: "a", Dep: "b"}, want: `workflow: duplicate dependency: step "a" depends on "b"`},
		{err: &workflow.ConfigError{Err: errors.New("other")}, want: "other"},
	}
	for _, test := range cases {
		if got := test.err.Error(); got != test.want {
			t.Errorf("Error() = %q, want %q", got, test.want)
		}
		if test.err.Unwrap() != test.err.Err {
			t.Fatal("Unwrap returned another error")
		}
	}
}

func TestResult_String(t *testing.T) {
	cases := map[workflow.Result]string{
		workflow.ResultPending: "pending",
		workflow.ResultSuccess: "success",
		workflow.ResultFailure: "failure",
		workflow.ResultSkipped: "skipped",
		workflow.Result(255):   "unknown",
	}
	for result, want := range cases {
		if got := result.String(); got != want {
			t.Errorf("Result(%d).String() = %q, want %q", result, got, want)
		}
	}
}
