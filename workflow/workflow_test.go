package workflow_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/workflow"
)

func runtimeGosched() { runtime.Gosched() }

func TestWorkflow_LinearChainSuccess(t *testing.T) {
	var order []string
	mk := func(name string) cron.Job {
		return cron.JobFunc(func(ctx context.Context) error {
			order = append(order, name)
			return nil
		})
	}
	w := workflow.MustNew(
		workflow.NewStep("A", mk("A")),
		workflow.NewStep("B", mk("B"), workflow.After("A", workflow.OnSuccess)),
		workflow.NewStep("C", mk("C"), workflow.After("B", workflow.OnSuccess)),
	)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	want := []string{"A", "B", "C"}
	if len(order) != 3 {
		t.Fatalf("order = %v", order)
	}
	for i, name := range want {
		if order[i] != name {
			t.Fatalf("order[%d] = %q, want %q (full: %v)", i, order[i], name, order)
		}
	}
}

func TestWorkflow_FailurePropagatesAsSkipDownstream(t *testing.T) {
	boom := errors.New("boom")
	var ranC atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return boom })),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error { return nil }),
			workflow.After("A", workflow.OnSuccess)),
		workflow.NewStep("C", cron.JobFunc(func(ctx context.Context) error { ranC.Store(true); return nil }),
			workflow.After("B", workflow.OnSuccess)),
	)
	err := w.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if ranC.Load() {
		t.Fatal("C should be skipped because B was skipped because A failed")
	}
}

func TestWorkflow_OnFailureRunsOnlyOnFailure(t *testing.T) {
	boom := errors.New("boom")
	var ranRecover atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return boom })),
		workflow.NewStep("recover", cron.JobFunc(func(ctx context.Context) error {
			ranRecover.Store(true)
			return nil
		}), workflow.After("A", workflow.OnFailure)),
	)
	_ = w.Run(context.Background())
	if !ranRecover.Load() {
		t.Fatal("recover step should run when A failed (OnFailure)")
	}
}

func TestWorkflow_OnCompleteRunsRegardless(t *testing.T) {
	var ranCleanup atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error {
			return errors.New("doesn't matter")
		})),
		workflow.NewStep("cleanup", cron.JobFunc(func(ctx context.Context) error {
			ranCleanup.Store(true)
			return nil
		}), workflow.After("A", workflow.OnComplete)),
	)
	_ = w.Run(context.Background())
	if !ranCleanup.Load() {
		t.Fatal("cleanup step should run regardless of A's outcome (OnComplete)")
	}
}

func TestWorkflow_OnSkippedRunsWhenDependencySkipped(t *testing.T) {
	var ranSkipped atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error {
			return errors.New("boom")
		})),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error {
			t.Fatal("B should be skipped because A failed")
			return nil
		}), workflow.After("A", workflow.OnSuccess)),
		workflow.NewStep("skipped-handler", cron.JobFunc(func(ctx context.Context) error {
			ranSkipped.Store(true)
			return nil
		}), workflow.After("B", workflow.OnSkipped)),
	)
	_ = w.Run(context.Background())
	if !ranSkipped.Load() {
		t.Fatal("OnSkipped step should run when dependency was skipped")
	}
}

func TestWorkflow_PanicCaughtAsFailure(t *testing.T) {
	w := workflow.MustNew(
		workflow.NewStep("boom", cron.JobFunc(func(ctx context.Context) error {
			panic("custom-detail")
		})),
	)
	err := w.Run(context.Background())
	if err == nil {
		t.Fatal("expected panic to surface as error")
	}
	if !strings.Contains(err.Error(), `step "boom" panicked`) || !strings.Contains(err.Error(), "custom-detail") {
		t.Fatalf("panic error lost context: %v", err)
	}
}

func TestWorkflow_ErrJoinsMultipleFailures(t *testing.T) {
	errA := errors.New("a failed")
	errB := errors.New("b failed")
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return errA })),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error { return errB })),
	)
	err := w.Run(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("joined err = %v, want both failures", err)
	}
}

func TestWorkflow_WithOnCompleteReceivesExecutionAndLeavesOriginalUnchanged(t *testing.T) {
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
	)
	var calls atomic.Int32
	w2 := w.WithOnComplete(func(exec *workflow.Execution) {
		calls.Add(1)
		if exec.ID == "" {
			t.Error("execution ID should be populated")
		}
		if got := exec.Results["A"]; got != workflow.ResultSuccess {
			t.Errorf("A result = %v, want success", got)
		}
		if len(exec.Errors) != 0 {
			t.Errorf("unexpected errors: %v", exec.Errors)
		}
	})

	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("WithOnComplete mutated the original workflow")
	}
	if err := w2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("onComplete calls = %d, want 1", calls.Load())
	}
}

func TestWorkflow_ContextCancellationReleasesWaitingSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	w := workflow.MustNew(
		workflow.NewStep("block", cron.JobFunc(func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})),
		workflow.NewStep("after", cron.JobFunc(func(ctx context.Context) error {
			t.Fatal("after should be skipped after cancellation")
			return nil
		}), workflow.After("block", workflow.OnSuccess)),
	)

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWorkflow_WaitsForAllDepsBeforeSkipping(t *testing.T) {
	releaseB := make(chan struct{})
	var bRunning atomic.Bool
	var observedBStillRunning atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error {
			return errors.New("A fails fast")
		})),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error {
			bRunning.Store(true)
			<-releaseB
			bRunning.Store(false)
			return nil
		})),
		workflow.NewStep("C", cron.JobFunc(func(ctx context.Context) error {
			t.Fatal("C should never start (A failed)")
			return nil
		}), workflow.After("A", workflow.OnSuccess), workflow.After("B", workflow.OnSuccess)),
		workflow.NewStep("downstream", cron.JobFunc(func(ctx context.Context) error {
			if bRunning.Load() {
				observedBStillRunning.Store(true)
			}
			return nil
		}), workflow.After("C", workflow.OnSkipped)),
	)

	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background()) }()

	for range 5 {
		runtimeGosched()
	}
	close(releaseB)
	<-done

	if observedBStillRunning.Load() {
		t.Fatal("downstream OnSkipped fired while B was still running — DAG barrier violated")
	}
}

func TestWorkflow_HonoursPreCancelledCtx(t *testing.T) {
	var ran atomic.Bool
	w := workflow.MustNew(
		workflow.NewStep("root", cron.JobFunc(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = w.Run(ctx)
	if ran.Load() {
		t.Fatal("root step ran despite pre-cancelled ctx")
	}
}

func TestWorkflow_DepsCopiedOnConstruction(t *testing.T) {
	var ranC atomic.Bool
	deps := []workflow.Dep{workflow.After("A", workflow.OnSuccess)}
	w := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
		workflow.Step{Name: "B", Job: cron.JobFunc(func(ctx context.Context) error { return nil }), Deps: deps},
		workflow.Step{Name: "C", Job: cron.JobFunc(func(ctx context.Context) error {
			ranC.Store(true)
			return nil
		}), Deps: deps},
	)
	deps[0] = workflow.After("ghost", workflow.OnSuccess)

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if !ranC.Load() {
		t.Fatal("C did not run — caller mutation leaked into the graph")
	}
}

func TestWorkflow_NewDuplicateStep(t *testing.T) {
	_, err := workflow.New(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
	)
	if !errors.Is(err, workflow.ErrDuplicateStep) {
		t.Fatalf("err = %v, want errors.Is ErrDuplicateStep", err)
	}
	var ce *workflow.ConfigError
	if !errors.As(err, &ce) || ce.Step != "A" {
		t.Fatalf("ConfigError = %+v, want Step=\"A\"", ce)
	}
}

func TestWorkflow_NewUnknownDep(t *testing.T) {
	_, err := workflow.New(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil }),
			workflow.After("ghost", workflow.OnSuccess)),
	)
	if !errors.Is(err, workflow.ErrUnknownDep) {
		t.Fatalf("err = %v, want errors.Is ErrUnknownDep", err)
	}
	var ce *workflow.ConfigError
	if !errors.As(err, &ce) || ce.Step != "A" || ce.Dep != "ghost" {
		t.Fatalf("ConfigError = %+v, want Step=\"A\" Dep=\"ghost\"", ce)
	}
}

func TestWorkflow_NewCycle(t *testing.T) {
	_, err := workflow.New(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil }),
			workflow.After("B", workflow.OnSuccess)),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error { return nil }),
			workflow.After("A", workflow.OnSuccess)),
	)
	if !errors.Is(err, workflow.ErrCycle) {
		t.Fatalf("err = %v, want errors.Is ErrCycle", err)
	}
}

func TestConfigError_ErrorMessage(t *testing.T) {
	cases := []struct {
		err  *workflow.ConfigError
		want string
	}{
		{&workflow.ConfigError{Err: workflow.ErrDuplicateStep, Step: "A"},
			`workflow: duplicate step: "A"`},
		{&workflow.ConfigError{Err: workflow.ErrUnknownDep, Step: "A", Dep: "ghost"},
			`workflow: unknown dependency: step "A" depends on "ghost"`},
		{&workflow.ConfigError{Err: workflow.ErrCycle, Step: "A", Dep: "B"},
			`workflow: cycle detected: "A" -> "B"`},
		{&workflow.ConfigError{Err: errors.New("other")}, `other`},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestWorkflow_MustNewPanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustNew should panic on duplicate step")
		}
	}()
	workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error { return nil })),
	)
}

func TestResult_String(t *testing.T) {
	cases := map[workflow.Result]string{
		workflow.ResultPending: "pending",
		workflow.ResultSuccess: "success",
		workflow.ResultFailure: "failure",
		workflow.ResultSkipped: "skipped",
		workflow.Result(255):   "unknown",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Fatalf("%d: %q, want %q", r, got, want)
		}
	}
}

func TestCondition_MatchUnknownReturnsFalse(t *testing.T) {
	w := workflow.MustNew(
		workflow.NewStep("A",
			cron.JobFunc(func(ctx context.Context) error { return nil })),
		workflow.NewStep("B",
			cron.JobFunc(func(ctx context.Context) error {
				t.Fatal("B must not run when dep condition is unknown")
				return nil
			}),
			workflow.After("A", workflow.Condition(99))),
	)
	_ = w.Run(context.Background())
}
