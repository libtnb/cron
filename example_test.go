package cron_test

import (
	"context"
	"fmt"

	"github.com/libtnb/cron"
)

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
