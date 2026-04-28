package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	c := cron.New(cron.WithLogger(logger), cron.WithLocation(time.UTC))

	id, err := c.Add("@every 2s", cron.JobFunc(func(ctx context.Context) error {
		logger.LogAttrs(ctx, slog.LevelInfo, "ran",
			slog.Time("at", time.Now()),
		)
		if time.Now().Second()%10 == 0 {
			return errors.New("simulated failure")
		}
		return nil
	}), cron.WithName("ticker"))
	if err != nil {
		logger.Error("add failed", slog.Any("err", err))
		os.Exit(1)
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "registered",
		slog.Any("entry_id", id),
	)

	if e, ok := c.Entry(id); ok {
		logger.LogAttrs(context.Background(), slog.LevelInfo, "entry snapshot",
			slog.Any("entry", e),
		)
	}

	if err := c.Start(); err != nil {
		logger.Error("start failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-ctx.Done()
}
