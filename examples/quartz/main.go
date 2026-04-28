package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/parserext"
)

func main() {
	c := cron.New(
		cron.WithLogger(slog.Default()),
		cron.WithParser(parserext.NewQuartzParser(time.UTC)),
	)

	_, err := c.Add("0 0 22 ? * 5L", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("last Friday of the month, 22:00 UTC")
		return nil
	}), cron.WithName("monthly-last-friday"))
	if err != nil {
		panic(err)
	}

	if err := c.Start(); err != nil {
		panic(err)
	}
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-ctx.Done()
	_ = c.Stop(context.Background())
}
