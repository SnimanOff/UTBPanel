package users

import "time"

// Days is the activity bitmap of a user: 128 bits, bit i means "active on
// day anchor-i". 16 bytes per user give exact retention for 128 days.
const Days = 128

// DayOf returns the UTC day number of t.
func DayOf(t time.Time) int64 { return t.UTC().Unix() / 86400 }

// Mark sets day d in the bitmap, moving the anchor forward when d is newer.
// Days older than the window are ignored.
func Mark(bits []byte, anchor, d int64) ([]byte, int64) {
	out := make([]byte, Days/8)
	copy(out, bits)
	if anchor == 0 {
		anchor = d
	}
	if d > anchor {
		out = shift(out, int(d-anchor))
		anchor = d
	}
	i := anchor - d
	if i >= 0 && i < Days {
		out[i/8] |= 1 << (i % 8)
	}
	return out, anchor
}

// shift moves every bit n positions to older days.
func shift(bits []byte, n int) []byte {
	out := make([]byte, Days/8)
	if n >= Days {
		return out
	}
	for i := 0; i < Days-n; i++ {
		if bits[i/8]&(1<<(i%8)) != 0 {
			j := i + n
			out[j/8] |= 1 << (j % 8)
		}
	}
	return out
}

// Active reports whether day d is set.
func Active(bits []byte, anchor, d int64) bool {
	i := anchor - d
	if i < 0 || i >= Days || len(bits) < Days/8 {
		return false
	}
	return bits[i/8]&(1<<(i%8)) != 0
}

// CountActive returns the number of active days in the window.
func CountActive(bits []byte) int {
	n := 0
	for _, b := range bits {
		for ; b != 0; b &= b - 1 {
			n++
		}
	}
	return n
}
