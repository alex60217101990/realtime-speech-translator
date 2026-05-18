// Package ringbuf is a single-producer / single-consumer lock-free
// ring buffer of float32 PCM samples. It backs the TTS → playback
// stage so a host audio callback (the consumer) never blocks waiting
// on the synthesis goroutine (the producer) and vice versa.
//
// The design is the classic monotonic-counter ring:
//
//   - capacity rounds up to a power of two so position-to-index
//     becomes one `& mask` op.
//   - head and tail are 64-bit monotonically-increasing positions —
//     occupancy is (head − tail), never wraps inside human lifetimes
//     even at megasample rates.
//   - head is touched only by the producer; tail only by the
//     consumer. Each side pads its counter to a cache line to avoid
//     false sharing.
//   - On amd64 atomic.Load/Store give us acquire/release ordering
//     for free, so no manual fences are needed.
package ringbuf

import (
	"sync/atomic"
)

// cacheLine is the assumed L1 cache line size on amd64 (and the
// other archs we ship to). 64 bytes is the dominant value; if the
// host happens to be larger (Apple Silicon: 128) the only cost is
// slightly higher memory use per Ring, never incorrect behaviour.
const cacheLine = 64

// Ring is an SPSC float32 ring buffer.
//
// Concurrency contract:
//
//   - Exactly one goroutine may call Write at a time.
//   - Exactly one goroutine may call Read at a time.
//   - Either side may also call Len, Cap and Reset (with the obvious
//     caveat that Reset is racy unless both producer and consumer
//     are quiesced).
type Ring struct {
	buf  []float32
	mask uint64

	_    [cacheLine - 8]byte
	head atomic.Uint64 // producer-only writer
	_    [cacheLine - 8]byte
	tail atomic.Uint64 // consumer-only writer
}

// nextPow2 returns the smallest power of two >= n. n = 0 returns 1.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	v := uint(n - 1)
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v |= v >> 32
	return int(v + 1)
}

// New constructs a Ring whose usable capacity is the next power of
// two ≥ capacity. The buffer slice is allocated once and never grows.
func New(capacity int) *Ring {
	n := nextPow2(capacity)
	return &Ring{
		buf:  make([]float32, n),
		mask: uint64(n - 1),
	}
}

// Cap returns the buffer's total sample capacity (always a power of
// two, may exceed the value passed to New).
func (r *Ring) Cap() int { return len(r.buf) }

// Len returns the number of samples currently available to Read.
// Safe to call from either side; the value is a snapshot.
func (r *Ring) Len() int {
	head := r.head.Load()
	tail := r.tail.Load()
	return int(head - tail)
}

// Reset clears the buffer state. Must be called only when neither
// producer nor consumer is active.
func (r *Ring) Reset() {
	r.head.Store(0)
	r.tail.Store(0)
}

// Write copies up to len(samples) entries into the ring and returns
// the count actually written. A short write means the buffer is full
// for the rest; the producer typically retries later or drops the
// remainder.
//
// Allocation-free: the only memory touched is r.buf.
func (r *Ring) Write(samples []float32) int {
	capN := uint64(len(r.buf))
	head := r.head.Load()
	// Consumer's tail is the only field we need to acquire-load to
	// learn what space is free.
	tail := r.tail.Load()

	avail := capN - (head - tail)
	n := min(uint64(len(samples)), avail)
	if n == 0 {
		return 0
	}

	headIdx := head & r.mask
	end := headIdx + n
	if end <= capN {
		copy(r.buf[headIdx:end], samples[:n])
	} else {
		// Wraparound: two contiguous segments.
		first := capN - headIdx
		copy(r.buf[headIdx:], samples[:first])
		copy(r.buf[:n-first], samples[first:n])
	}

	r.head.Store(head + n)
	return int(n)
}

// Read fills dst with up to len(dst) samples and returns the count
// actually read. A short read means the buffer is empty for the
// rest; the audio callback typically pads the remainder with zeros.
func (r *Ring) Read(dst []float32) int {
	capN := uint64(len(r.buf))
	head := r.head.Load()
	tail := r.tail.Load()

	avail := head - tail
	n := min(uint64(len(dst)), avail)
	if n == 0 {
		return 0
	}

	tailIdx := tail & r.mask
	end := tailIdx + n
	if end <= capN {
		copy(dst[:n], r.buf[tailIdx:end])
	} else {
		first := capN - tailIdx
		copy(dst[:first], r.buf[tailIdx:])
		copy(dst[first:n], r.buf[:n-first])
	}

	r.tail.Store(tail + n)
	return int(n)
}
