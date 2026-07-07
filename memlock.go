package cron

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// MemoryLocker is a process-local Locker for tests and single-process
// composition. Unlike backend lockers it does not retain claims after
// release: a single scheduler never dispatches the same fire twice, so
// in-process retention is unnecessary. It is not a substitute for a backend
// locker across processes.
type MemoryLocker struct {
	mu   sync.Mutex
	held map[string]struct{}
}

func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{held: make(map[string]struct{})}
}

func (l *MemoryLocker) Lock(_ context.Context, key string) (ReleaseFunc, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.held[key]; ok {
		return nil, fmt.Errorf("%w: %s", ErrLockHeld, key)
	}
	l.held[key] = struct{}{}
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			l.mu.Lock()
			delete(l.held, key)
			l.mu.Unlock()
		})
		return nil
	}, nil
}

// MemoryElector is a settable Elector for tests and single-process use. A
// new MemoryElector starts as leader.
type MemoryElector struct {
	notLeader atomic.Bool
}

func NewMemoryElector() *MemoryElector { return &MemoryElector{} }

// SetLeader flips this instance's leadership.
func (e *MemoryElector) SetLeader(leader bool) { e.notLeader.Store(!leader) }

func (e *MemoryElector) IsLeader(context.Context) error {
	if e.notLeader.Load() {
		return ErrNotLeader
	}
	return nil
}
