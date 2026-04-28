// Package heap provides a typed min-heap with addressable items.
package heap

// Item is a heap entry returned by Push and accepted by Fix/Remove.
type Item[T any] struct {
	Key   int64
	Value T
	idx   int
}

// Index reports the item's heap position, or -1 after removal.
func (it *Item[T]) Index() int { return it.idx }

// Heap is a min-heap keyed by int64. The zero value is usable.
type Heap[T any] struct {
	items []*Item[T]
}

func New[T any]() *Heap[T] { return &Heap[T]{} }

func (h *Heap[T]) Len() int { return len(h.items) }

func (h *Heap[T]) Push(key int64, v T) *Item[T] {
	it := &Item[T]{Key: key, Value: v, idx: len(h.items)}
	h.items = append(h.items, it)
	h.up(it.idx)
	return it
}

func (h *Heap[T]) Peek() (*Item[T], bool) {
	if len(h.items) == 0 {
		return nil, false
	}
	return h.items[0], true
}

func (h *Heap[T]) Pop() (*Item[T], bool) {
	n := len(h.items)
	if n == 0 {
		return nil, false
	}
	top := h.items[0]
	last := n - 1
	if last > 0 {
		h.items[0] = h.items[last]
		h.items[0].idx = 0
	}
	h.items[last] = nil // help GC
	h.items = h.items[:last]
	if last > 0 {
		h.down(0)
	}
	top.idx = -1
	return top, true
}

func (h *Heap[T]) Fix(it *Item[T], newKey int64) {
	if it.idx < 0 || it.idx >= len(h.items) || h.items[it.idx] != it {
		return
	}
	it.Key = newKey
	if !h.down(it.idx) {
		h.up(it.idx)
	}
}

func (h *Heap[T]) Remove(it *Item[T]) {
	if it.idx < 0 || it.idx >= len(h.items) || h.items[it.idx] != it {
		return
	}
	n := len(h.items) - 1
	removedIdx := it.idx
	if removedIdx != n {
		h.swap(removedIdx, n)
	}
	h.items[n] = nil // help GC
	h.items = h.items[:n]
	if removedIdx < len(h.items) {
		if !h.down(removedIdx) {
			h.up(removedIdx)
		}
	}
	it.idx = -1
}

func (h *Heap[T]) less(i, j int) bool { return h.items[i].Key < h.items[j].Key }

func (h *Heap[T]) swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].idx = i
	h.items[j].idx = j
}

func (h *Heap[T]) up(i int) {
	for {
		parent := (i - 1) / 2
		if parent == i || !h.less(i, parent) {
			break
		}
		h.swap(i, parent)
		i = parent
	}
}

func (h *Heap[T]) down(i int) bool {
	n := len(h.items)
	start := i
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		smallest := left
		if right := left + 1; right < n && h.less(right, left) {
			smallest = right
		}
		if !h.less(smallest, i) {
			break
		}
		h.swap(i, smallest)
		i = smallest
	}
	return i > start
}
