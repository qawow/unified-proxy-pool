package chanpolicy

import "time"

// numBuckets splits the window into slices so old outcomes fade out gradually
// instead of the whole window clearing at once.
const numBuckets = 6

// bucket counts outcomes for one slice of wall-clock time. idx is the absolute
// slice number (unix/bucketSec) the counts belong to, which is what lets a stale
// bucket be recognized and reset instead of being read as current data.
type bucket struct {
	idx     int64
	ok      int
	fail    int
	timeout int
}

// counters is a fixed-size ring of buckets. Fixed size matters: there is one of
// these per (channel, proxy) pair, and the pair count is the product of two
// numbers the operator does not control.
type counters struct {
	buckets [numBuckets]bucket
	// consecFails is a streak, not a windowed sum, so it lives outside the ring.
	consecFails   int
	lastOutcomeAt time.Time
}

func bucketIndex(now time.Time, bucketSec int64) int64 {
	if bucketSec <= 0 {
		bucketSec = 1
	}
	return now.Unix() / bucketSec
}

// slot returns the ring position for an absolute bucket index, resetting it first
// if it still holds an older slice.
func (c *counters) slot(idx int64) *bucket {
	b := &c.buckets[int(idx%numBuckets+numBuckets)%numBuckets]
	if b.idx != idx {
		*b = bucket{idx: idx}
	}
	return b
}

// record adds one outcome. window is passed in so a hot-applied policy change
// takes effect without rebuilding every counter.
func (c *counters) record(now time.Time, bucketSec int64, ok, timedOut bool, staleAfter time.Duration) {
	idx := bucketIndex(now, bucketSec)
	b := c.slot(idx)
	if ok {
		b.ok++
		c.consecFails = 0
	} else {
		b.fail++
		if timedOut {
			b.timeout++
		}
		// A streak only counts as consecutive if the previous failure is still
		// recent. Two failures an hour apart are not a run of two.
		if !c.lastOutcomeAt.IsZero() && now.Sub(c.lastOutcomeAt) > staleAfter {
			c.consecFails = 1
		} else {
			c.consecFails++
		}
	}
	c.lastOutcomeAt = now
}

// sum totals the buckets still inside the window.
func (c *counters) sum(now time.Time, bucketSec int64) (ok, fail, timeout int) {
	cur := bucketIndex(now, bucketSec)
	oldest := cur - numBuckets + 1
	for i := range c.buckets {
		b := c.buckets[i]
		if b.idx < oldest || b.idx > cur {
			continue
		}
		ok += b.ok
		fail += b.fail
		timeout += b.timeout
	}
	return ok, fail, timeout
}

// consecutive reports the current failure streak, treating a streak whose last
// failure has aged out of the window as broken.
func (c *counters) consecutive(now time.Time, staleAfter time.Duration) int {
	if c.consecFails == 0 {
		return 0
	}
	if !c.lastOutcomeAt.IsZero() && now.Sub(c.lastOutcomeAt) > staleAfter {
		return 0
	}
	return c.consecFails
}

// empty reports whether every bucket has aged out and no streak is live, meaning
// the entry carries no information and can be evicted.
func (c *counters) empty(now time.Time, bucketSec int64) bool {
	ok, fail, timeout := c.sum(now, bucketSec)
	return ok == 0 && fail == 0 && timeout == 0
}
