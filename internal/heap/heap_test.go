package heap

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestHeap_Empty(t *testing.T) {
	h := New[int]()
	if h.Len() != 0 {
		t.Fatal("len")
	}
	if _, ok := h.Peek(); ok {
		t.Fatal("peek empty")
	}
	if _, ok := h.Pop(); ok {
		t.Fatal("pop empty")
	}
}

func TestHeap_PushPopOrder(t *testing.T) {
	h := New[int]()
	keys := []int64{5, 1, 3, 2, 4}
	for i, k := range keys {
		h.Push(k, i)
	}
	want := []int64{1, 2, 3, 4, 5}
	for _, w := range want {
		it, ok := h.Pop()
		if !ok || it.Key != w {
			t.Fatalf("got %v, want %d", it, w)
		}
	}
	if h.Len() != 0 {
		t.Fatal("not empty after popping all")
	}
}

func TestHeap_Peek(t *testing.T) {
	h := New[string]()
	h.Push(10, "a")
	h.Push(5, "b")
	h.Push(7, "c")
	it, ok := h.Peek()
	if !ok || it.Key != 5 || it.Value != "b" {
		t.Fatalf("peek = %v", it)
	}
	if h.Len() != 3 {
		t.Fatal("Peek must not pop")
	}
}

func TestHeap_FixIncrease(t *testing.T) {
	h := New[int]()
	a := h.Push(1, 0)
	h.Push(2, 1)
	h.Push(3, 2)
	h.Fix(a, 100)
	it, _ := h.Pop()
	if it.Key != 2 {
		t.Fatalf("after Fix(a, 100), pop = %d, want 2", it.Key)
	}
}

func TestHeap_FixDecrease(t *testing.T) {
	h := New[int]()
	h.Push(10, 0)
	h.Push(20, 1)
	c := h.Push(30, 2)
	h.Fix(c, 1)
	it, _ := h.Pop()
	if it.Key != 1 {
		t.Fatalf("after Fix(c, 1), pop = %d, want 1", it.Key)
	}
}

func TestHeap_RemoveHead(t *testing.T) {
	h := New[int]()
	a := h.Push(1, 0)
	h.Push(2, 1)
	h.Push(3, 2)
	h.Remove(a)
	it, _ := h.Pop()
	if it.Key != 2 {
		t.Fatalf("got %d, want 2", it.Key)
	}
	if a.Index() != -1 {
		t.Fatalf("removed item idx = %d", a.Index())
	}
}

func TestHeap_RemoveMiddle(t *testing.T) {
	h := New[int]()
	h.Push(1, 0)
	b := h.Push(2, 1)
	h.Push(3, 2)
	h.Push(4, 3)
	h.Remove(b)
	got := []int64{}
	for {
		it, ok := h.Pop()
		if !ok {
			break
		}
		got = append(got, it.Key)
	}
	want := []int64{1, 3, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHeap_RemoveTail(t *testing.T) {
	h := New[int]()
	h.Push(1, 0)
	h.Push(2, 1)
	c := h.Push(3, 2)
	h.Remove(c)
	got := []int64{}
	for {
		it, ok := h.Pop()
		if !ok {
			break
		}
		got = append(got, it.Key)
	}
	if !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("got %v", got)
	}
}

func TestHeap_RemoveTwiceIsNoop(t *testing.T) {
	h := New[int]()
	a := h.Push(1, 0)
	h.Push(2, 1)
	h.Remove(a)
	h.Remove(a) // must not panic
	if h.Len() != 1 {
		t.Fatal("len")
	}
}

func TestHeap_FixOnRemovedIsNoop(t *testing.T) {
	h := New[int]()
	a := h.Push(1, 0)
	h.Remove(a)
	h.Fix(a, 100) // must not panic
}

func TestHeap_RandomFuzz(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	h := New[int]()
	const n = 1000
	keys := make([]int64, n)
	items := make([]*Item[int], n)
	for i := range n {
		keys[i] = int64(r.IntN(100000))
		items[i] = h.Push(keys[i], i)
	}
	for range n / 2 {
		i := r.IntN(n)
		if items[i].Index() >= 0 {
			h.Remove(items[i])
			keys[i] = -1
		}
	}
	var got []int64
	for {
		it, ok := h.Pop()
		if !ok {
			break
		}
		got = append(got, it.Key)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("pop order not sorted")
	}
	expected := 0
	for _, k := range keys {
		if k >= 0 {
			expected++
		}
	}
	if len(got) != expected {
		t.Fatalf("got %d items, want %d", len(got), expected)
	}
}
