package cron_test

import (
	"context"
	"fmt"
	"time"

	"github.com/libtnb/cron"
)

// ExampleNew registers a job, starts the scheduler, runs the job on demand and
// shuts down.
func ExampleNew() {
	c, err := cron.New(cron.WithLocation(time.UTC))
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	id, err := c.Add("0 9 * * MON-FRI", cron.JobFunc(func(ctx context.Context) error {
		info, _ := cron.EntryInfoFromContext(ctx)
		fmt.Println("running", info.Name)
		return nil
	}), cron.WithName("digest"))
	if err != nil {
		fmt.Println("add:", err)
		return
	}
	if err := c.Start(); err != nil {
		fmt.Println("start:", err)
		return
	}
	// Run the entry now instead of waiting for 09:00.
	if err := c.TriggerAndWait(context.Background(), id); err != nil {
		fmt.Println("trigger:", err)
	}
	if err := c.Stop(context.Background()); err != nil {
		fmt.Println("stop:", err)
	}
	// Output: running digest
}

func ExampleValidateSpec() {
	fmt.Println(cron.ValidateSpec("*/15 * * * *"))
	fmt.Println(cron.ValidateSpec("61 * * * *"))
	// Output:
	// <nil>
	// cron: parse "61 * * * *": field "minute": 61 above maximum 59
}

func ExampleAnalyzeSpec() {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

	a := cron.AnalyzeSpec("CRON_TZ=UTC 30 8 * * *", now)
	fmt.Println(a.Valid, a.Location, a.NextRun.Format(time.RFC3339))

	a = cron.AnalyzeSpec("@every 90s", now)
	fmt.Println(a.Valid, a.Descriptor, a.Interval)
	// Output:
	// true UTC 2026-01-02T08:30:00Z
	// true @every 1m30s
}

func ExampleNextN() {
	s, err := cron.NewStandardParser(cron.WithDefaultLocation(time.UTC)).Parse("0 9 * * MON-FRI")
	if err != nil {
		fmt.Println(err)
		return
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) // a Thursday
	for _, t := range cron.NextN(s, from, 3) {
		fmt.Println(t.Format("Mon 2006-01-02 15:04"))
	}
	// Output:
	// Thu 2026-01-01 09:00
	// Fri 2026-01-02 09:00
	// Mon 2026-01-05 09:00
}

func ExampleBetween() {
	s, err := cron.NewStandardParser(cron.WithDefaultLocation(time.UTC)).Parse("0 */6 * * *")
	if err != nil {
		fmt.Println(err)
		return
	}
	start := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	for t := range cron.Between(s, start, start.Add(24*time.Hour)) {
		fmt.Println(t.Format(time.RFC3339))
	}
	// Output:
	// 2026-03-01T06:00:00Z
	// 2026-03-01T12:00:00Z
	// 2026-03-01T18:00:00Z
	// 2026-03-02T00:00:00Z
}

func ExampleJobFunc() {
	j := cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("hello")
		return nil
	})
	_ = j.Run(context.Background())
	// Output: hello
}

func ExampleChain() {
	mk := func(name string) cron.Wrapper {
		return func(j cron.Job) cron.Job {
			return cron.JobFunc(func(ctx context.Context) error {
				fmt.Println("enter", name)
				err := j.Run(ctx)
				fmt.Println("leave", name)
				return err
			})
		}
	}
	core := cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("run core")
		return nil
	})
	_ = cron.Chain(mk("outer"), mk("inner"))(core).Run(context.Background())
	// Output:
	// enter outer
	// enter inner
	// run core
	// leave inner
	// leave outer
}
