// Package metrics holds what the core and the panel share about metrics:
// the latency histogram and the minute record. Latencies are kept as
// histograms, not ready percentiles: percentiles cannot be averaged, but
// histograms add up, so p95 is honest over any period.
package metrics

import "time"

// Bounds are the upper bounds of latency buckets in milliseconds; the last
// bucket is everything above.
var Bounds = []float64{1, 2, 5, 10, 20, 50, 100, 200, 300, 500, 750, 1000, 1500, 2000, 3000, 5000, 10000, 20000, 30000}

// NumBuckets is len(Bounds)+1.
var NumBuckets = len(Bounds) + 1

// Bucket returns the bucket of a duration.
func Bucket(d time.Duration) int {
	ms := float64(d) / float64(time.Millisecond)
	for i, b := range Bounds {
		if ms <= b {
			return i
		}
	}
	return len(Bounds)
}

// Hist is a latency histogram.
type Hist []uint64

// NewHist returns an empty histogram.
func NewHist() Hist { return make(Hist, NumBuckets) }

// Add records a duration.
func (h Hist) Add(d time.Duration) { h[Bucket(d)]++ }

// Merge adds o into h.
func (h Hist) Merge(o Hist) Hist {
	if len(h) < NumBuckets {
		h = append(h, make(Hist, NumBuckets-len(h))...)
	}
	for i, v := range o {
		if i < len(h) {
			h[i] += v
		}
	}
	return h
}

// Count is the number of samples.
func (h Hist) Count() uint64 {
	var n uint64
	for _, v := range h {
		n += v
	}
	return n
}

// Percentile returns the q-th quantile (0..1) in milliseconds, using linear
// interpolation inside the bucket.
func (h Hist) Percentile(q float64) float64 {
	total := h.Count()
	if total == 0 {
		return 0
	}
	rank := q * float64(total)
	var seen float64
	for i, v := range h {
		if v == 0 {
			continue
		}
		if seen+float64(v) >= rank {
			lo := 0.0
			if i > 0 {
				lo = Bounds[i-1]
			}
			hi := lo * 2
			if i < len(Bounds) {
				hi = Bounds[i]
			}
			return lo + (hi-lo)*(rank-seen)/float64(v)
		}
		seen += float64(v)
	}
	return Bounds[len(Bounds)-1]
}
