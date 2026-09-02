// Package parsecache memoizes parser results so repeated registrations of the
// same spec share one parsed value and pay the parse cost once.
package parsecache

import (
	"sync"
	"sync/atomic"
)

// ParseFunc produces a value. The cache stores builders wrapped in
// sync.OnceValues, so concurrent first callers share a single build.
type ParseFunc[T any] func() (T, error)

// Cache is a typed memoizing store. The zero value is ready and unbounded. It
// is safe for concurrent use.
type Cache[T any] struct {
	// Limit softly caps stored entries; <= 0 means unbounded. When full, Get
	// builds without memoizing.
	Limit int64

	m sync.Map // string → ParseFunc[T]
	n atomic.Int64
}

// Get returns the memoized result for spec, building it with build on first
// use; concurrent callers for the same spec share one build. Errors are
// memoized too, so a caller that does not want to pin a failure must Forget
// it. Once Limit is reached the value is built without being stored.
func (c *Cache[T]) Get(spec string, build func() (T, error)) (T, error) {
	if v, ok := c.m.Load(spec); ok {
		return v.(ParseFunc[T])()
	}
	if c.Limit > 0 && c.n.Load() >= c.Limit {
		return build()
	}
	once := ParseFunc[T](sync.OnceValues(build))
	actual, loaded := c.m.LoadOrStore(spec, once)
	if !loaded {
		c.n.Add(1)
	}
	return actual.(ParseFunc[T])()
}

// Len reports the number of stored entries. It walks the map, so it is meant
// for tests and diagnostics rather than hot paths.
func (c *Cache[T]) Len() int {
	var n int
	c.m.Range(func(any, any) bool { n++; return true })
	return n
}

// Forget drops spec's memoized result, if any, freeing its Limit slot.
func (c *Cache[T]) Forget(spec string) {
	if _, ok := c.m.LoadAndDelete(spec); ok {
		c.n.Add(-1)
	}
}
