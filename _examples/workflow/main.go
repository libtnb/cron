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
	c := cron.MustNew(cron.WithLogger(slog.Default()))

	builder := workflow.New()
	a := builder.Job("A", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("A done")
		return nil
	}))
	b := builder.Job("B", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("B done")
		return nil
	}), workflow.After(a, workflow.OnSuccess))
	cStep := builder.Job("C", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("C done")
		return nil
	}), workflow.After(a, workflow.OnSuccess))
	builder.Job("D", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("D done")
		return nil
	}), workflow.After(b, workflow.OnComplete), workflow.After(cStep, workflow.OnComplete))
	wf := builder.MustBuild().WithOnComplete(func(e *workflow.Execution) {
		fmt.Printf("workflow %s done: %v\n", e.ID, e.Results())
	})

	_, _ = c.AddSchedule(cron.TriggeredSchedule(), wf, cron.WithName("dag"))
	if err := c.Start(); err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		time.Sleep(500 * time.Millisecond)
		_, _ = c.TriggerByName("dag")
	}()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(shutdownCtx)
}
