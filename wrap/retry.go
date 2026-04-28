package wrap

import "github.com/libtnb/cron"

// Retry is shorthand for p.Wrapper().
func Retry(p cron.RetryPolicy) cron.Wrapper { return p.Wrapper() }
