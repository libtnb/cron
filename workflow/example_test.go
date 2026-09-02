package workflow_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/workflow"
)

// ExampleBuilder wires two typed steps; the second reads the first's output.
func ExampleBuilder() {
	b := workflow.New(workflow.WithMaxParallelism(4))

	fetch := b.Step[int]("fetch", func(ctx context.Context, _ workflow.Inputs) (int, error) {
		return 42, nil
	})
	store := b.Step[string]("store", func(ctx context.Context, in workflow.Inputs) (string, error) {
		n, ok := in.Get(fetch)
		if !ok {
			return "", errors.New("fetch output unavailable")
		}
		return fmt.Sprintf("stored %d", n), nil
	}, workflow.After(fetch, workflow.OnSuccess))

	wf, err := b.Build()
	if err != nil {
		fmt.Println("build:", err)
		return
	}
	exec := wf.Execute(context.Background())
	result, _ := exec.Get(store)
	fmt.Println(result)
	fmt.Println(exec.Results()["fetch"], exec.Results()["store"], exec.Err())
	// Output:
	// stored 42
	// success success <nil>
}

// ExampleBuilder_onFailure runs a fallback only when the primary step fails.
func ExampleBuilder_onFailure() {
	b := workflow.New()
	primary := b.Job("primary", cron.JobFunc(func(ctx context.Context) error {
		return errors.New("upstream down")
	}))
	b.Job("fallback", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("fallback ran")
		return nil
	}), workflow.After(primary, workflow.OnFailure))
	b.Job("report", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("report ran")
		return nil
	}), workflow.After(primary, workflow.OnSuccess))

	exec := b.MustBuild().Execute(context.Background())
	fmt.Println(exec.Results()["fallback"], exec.Results()["report"])
	fmt.Println(exec.Err())
	// Output:
	// fallback ran
	// success skipped
	// upstream down
}
