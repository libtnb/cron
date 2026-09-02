package wrap

import "github.com/libtnb/cron"

// Retry is shorthand for p.Wrapper(), for use in cron.WithChain and
// cron.WithEntryChain lists. cron.WithRetry and cron.WithEntryRetry install
// the same wrapper innermost automatically.
func Retry(p cron.RetryPolicy) cron.Wrapper { return p.Wrapper() }
