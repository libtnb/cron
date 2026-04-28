package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/workflow"
)

func main() {
	c := cron.New(cron.WithLogger(slog.Default()))

	wf := workflow.MustNew(
		workflow.NewStep("A", cron.JobFunc(func(ctx context.Context) error {
			fmt.Println("A done")
			return nil
		})),
		workflow.NewStep("B", cron.JobFunc(func(ctx context.Context) error {
			fmt.Println("B done")
			return nil
		}), workflow.After("A", workflow.OnSuccess)),
		workflow.NewStep("C", cron.JobFunc(func(ctx context.Context) error {
			fmt.Println("C done")
			return nil
		}), workflow.After("A", workflow.OnSuccess)),
		workflow.NewStep("D", cron.JobFunc(func(ctx context.Context) error {
			fmt.Println("D done")
			return nil
		}), workflow.After("B", workflow.OnComplete), workflow.After("C", workflow.OnComplete)),
	).WithOnComplete(func(e *workflow.Execution) {
		fmt.Printf("workflow %s done: %v\n", e.ID, e.Results)
	})

	_, _ = c.AddSchedule(cron.TriggeredSchedule(), wf, cron.WithName("dag"))
	if err := c.Start(); err != nil {
		panic(err)
	}

	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go func() {
		time.Sleep(500 * time.Millisecond)
		_, _ = c.TriggerByName("dag")
	}()
	<-ctx.Done()
	_ = c.Stop(context.Background())
}
