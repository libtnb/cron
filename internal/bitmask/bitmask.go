// Package bitmask provides bit-set scans shared by the schedule
// implementations.
package bitmask

import "math/bits"

// NextInRange returns the lowest set bit of bm in [from, until], or -1.
func NextInRange(bm uint64, from, until uint) int {
	if from > until {
		return -1
	}
	masked := bm >> from << from
	if until < 63 {
		masked &= (uint64(1) << (until + 1)) - 1
	}
	if masked == 0 {
		return -1
	}
	return bits.TrailingZeros64(masked)
}
