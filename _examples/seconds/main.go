package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
)

func main() {
	c := cron.New(
		cron.WithLogger(slog.Default()),
		cron.WithParser(cron.NewStandardParser(cron.WithSeconds())),
	)

	if _, err := c.Add("*/5 * * * * *", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("every 5s tick", time.Now().Format(time.RFC3339))
		return nil
	}), cron.WithName("five-second")); err != nil {
		panic(err)
	}

	if _, err := c.Add("30 * * * * *", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("at :30 of every minute", time.Now().Format(time.RFC3339))
		return nil
	}), cron.WithName("half-minute")); err != nil {
		panic(err)
	}

	if err := c.Start(); err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(shutdownCtx)
}
