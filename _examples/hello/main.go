package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/wrap"
)

func main() {
	c := cron.New(
		cron.WithLogger(slog.Default()),
		cron.WithChain(wrap.Recover(), wrap.Timeout(30*time.Second)),
	)
	id, err := c.Add("@every 5s", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("tick", time.Now().Format(time.RFC3339))
		return nil
	}), cron.WithName("heartbeat"))
	if err != nil {
		panic(err)
	}
	fmt.Println("registered entry:", id)

	if err := c.Start(); err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	fmt.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(shutdownCtx)
}
