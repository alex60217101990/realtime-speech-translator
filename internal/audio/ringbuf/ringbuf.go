// Package ringbuf implements a single-producer single-consumer lock-free ring
// buffer of int16 audio samples sized to a power of two.
//
// It is intended for the audio capture hot path where allocations and blocking
// must be avoided. Producer writes from a hardware/DMA callback; consumer reads
// from a goroutine that feeds VAD and STT.
package ringbuf

import (
	"errors"
	"math/bits"
	"sync/atomic"
)

var (
	ErrSizeNotPow2  = errors.New("ringbuf: capacity must be a power of two")
	ErrSizeTooSmall = errors.New("ringbuf: capacity must be >= 2")
)

// Ring is a fixed-capacity SPSC ring buffer of int16 samples.
//
// Capacity must be a power of two so that index masking is a single AND.
// Head and tail are monotonically increasing 64-bit counters; the effective
// index into the underlying slice is counter & mask.
type Ring struct {
	buf  []int16
	mask uint64
	// head is the next write index (producer-owned, read by consumer).
	head atomic.Uint64
	// tail is the next read index (consumer-owned, read by producer).
	tail atomic.Uint64
}

// New returns a Ring with the given capacity. Capacity must be a power of two
// and at least 2.
func New(capacity int) (*Ring, error) {
	if capacity < 2 {
		return nil, ErrSizeTooSmall
	}
	if bits.OnesCount(uint(capacity)) != 1 {
		return nil, ErrSizeNotPow2
	}
	return &Ring{
		buf:  make([]int16, capacity),
		mask: uint64(capacity - 1),
	}, nil
}

// Cap returns the buffer capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Len returns the number of samples currently available to read. The value
// can change concurrently; treat it as a hint.
func (r *Ring) Len() int {
	return int(r.head.Load() - r.tail.Load())
}

// Write copies up to len(p) samples into the ring. Returns the number of
// samples actually written, which is min(len(p), Cap()-Len()). Non-blocking.
//
// Must be called from at most one goroutine (the producer).
func (r *Ring) Write(p []int16) int {
	head := r.head.Load()
	tail := r.tail.Load()
	free := uint64(len(r.buf)) - (head - tail)
	n := min(uint64(len(p)), free)
	if n == 0 {
		return 0
	}
	// Possibly two segments if write wraps around.
	idx := head & r.mask
	first := min(uint64(len(r.buf))-idx, n)
	copy(r.buf[idx:idx+first], p[:first])
	if rem := n - first; rem > 0 {
		copy(r.buf[:rem], p[first:n])
	}
	r.head.Store(head + n)
	return int(n)
}

// Read copies up to len(p) samples out of the ring. Returns the number
// actually read. Non-blocking.
//
// Must be called from at most one goroutine (the consumer).
func (r *Ring) Read(p []int16) int {
	head := r.head.Load()
	tail := r.tail.Load()
	avail := head - tail
	n := min(uint64(len(p)), avail)
	if n == 0 {
		return 0
	}
	idx := tail & r.mask
	first := min(uint64(len(r.buf))-idx, n)
	copy(p[:first], r.buf[idx:idx+first])
	if rem := n - first; rem > 0 {
		copy(p[first:n], r.buf[:rem])
	}
	r.tail.Store(tail + n)
	return int(n)
}

// Reset clears the buffer counters. Not safe to call concurrently with
// Read or Write.
func (r *Ring) Reset() {
	r.head.Store(0)
	r.tail.Store(0)
}
