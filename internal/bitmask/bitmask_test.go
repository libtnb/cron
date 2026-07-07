package bitmask

import "testing"

func TestNextInRange(t *testing.T) {
	cases := []struct {
		name        string
		bm          uint64
		from, until uint
		want        int
	}{
		{"from beyond until", 1 << 5, 6, 5, -1},
		{"lowest in range", 1<<3 | 1<<7, 2, 10, 3},
		{"skips below from", 1<<3 | 1<<7, 4, 10, 7},
		{"masked out above until", 1 << 7, 0, 6, -1},
		{"empty bitmap", 0, 0, 12, -1},
		{"until at word edge", 1 << 63, 0, 63, 63},
	}
	for _, c := range cases {
		if got := NextInRange(c.bm, c.from, c.until); got != c.want {
			t.Errorf("%s: NextInRange(%#x, %d, %d) = %d, want %d",
				c.name, c.bm, c.from, c.until, got, c.want)
		}
	}
}
