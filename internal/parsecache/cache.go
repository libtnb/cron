// Package parsecache memoises parser results and never evicts.
package parsecache

import "sync"

type ParseFunc[T any] func() (T, error)

// Cache is a typed memoising store. The zero value is ready.
type Cache[T any] struct {
	m sync.Map // string → ParseFunc[T]
}

// Get builds once per spec; concurrent callers share the same result.
func (c *Cache[T]) Get(spec string, build func() (T, error)) (T, error) {
	if v, ok := c.m.Load(spec); ok {
		return v.(ParseFunc[T])()
	}
	once := ParseFunc[T](sync.OnceValues(build))
	actual, _ := c.m.LoadOrStore(spec, once)
	return actual.(ParseFunc[T])()
}

func (c *Cache[T]) Lookup(spec string) (val T, err error, ok bool) {
	v, found := c.m.Load(spec)
	if !found {
		return val, nil, false
	}
	val, err = v.(ParseFunc[T])()
	return val, err, true
}

func (c *Cache[T]) Len() int {
	n := 0
	c.m.Range(func(any, any) bool { n++; return true })
	return n
}

func (c *Cache[T]) Forget(spec string) { c.m.Delete(spec) }
