// Package parsecache memoises parser results.
package parsecache

import (
	"sync"
	"sync/atomic"
)

type ParseFunc[T any] func() (T, error)

// Cache is a typed memoising store. The zero value is ready and unbounded.
type Cache[T any] struct {
	// Limit softly caps stored entries; <= 0 means unbounded. When full, Get
	// builds without memoising.
	Limit int64

	m sync.Map // string → ParseFunc[T]
	n atomic.Int64
}

// Get builds once per spec; concurrent callers share the same result.
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

// Len reports the number of stored entries.
func (c *Cache[T]) Len() int {
	n := 0
	c.m.Range(func(any, any) bool { n++; return true })
	return n
}

// Forget drops spec's memoised result.
func (c *Cache[T]) Forget(spec string) {
	if _, ok := c.m.LoadAndDelete(spec); ok {
		c.n.Add(-1)
	}
}
