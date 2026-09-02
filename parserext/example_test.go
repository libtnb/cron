package parserext_test

import (
	"fmt"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/parserext"
)

func ExampleNewQuartzParser() {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 22 ? * 5L") // 22:00 on the last Friday of each month
	if err != nil {
		fmt.Println(err)
		return
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, t := range cron.NextN(s, from, 2) {
		fmt.Println(t.Format("Mon 2006-01-02 15:04"))
	}
	// Output:
	// Fri 2026-01-30 22:00
	// Fri 2026-02-27 22:00
}
