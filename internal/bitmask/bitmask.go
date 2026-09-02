// Package bitmask provides the bit-set scan shared by the schedule
// implementations, which store each cron field as a uint64 mask.
package bitmask

import "math/bits"

// NextInRange returns the lowest set bit of bm at a position in [from, until],
// or -1 when none is set or from > until. until must be at most 63.
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
