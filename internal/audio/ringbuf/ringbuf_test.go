package ringbuf

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewValidation(t *testing.T) {
	if _, err := New(1); err == nil {
		t.Fatal("expected error for size 1")
	}
	if _, err := New(3); err == nil {
		t.Fatal("expected error for non-pow2 size")
	}
	if _, err := New(8); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteReadBasic(t *testing.T) {
	r, _ := New(8)
	in := []int16{1, 2, 3, 4, 5}
	if n := r.Write(in); n != 5 {
		t.Fatalf("write returned %d", n)
	}
	if got := r.Len(); got != 5 {
		t.Fatalf("len=%d", got)
	}
	out := make([]int16, 5)
	if n := r.Read(out); n != 5 {
		t.Fatalf("read returned %d", n)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("mismatch at %d: %d vs %d", i, in[i], out[i])
		}
	}
	if got := r.Len(); got != 0 {
		t.Fatalf("expected empty, got len=%d", got)
	}
}

func TestWraparound(t *testing.T) {
	r, _ := New(8)
	// Push 6, pop 6 to advance both indices past midpoint.
	first := []int16{10, 11, 12, 13, 14, 15}
	r.Write(first)
	tmp := make([]int16, 6)
	r.Read(tmp)
	// Now write 7 — this must wrap.
	in := []int16{20, 21, 22, 23, 24, 25, 26}
	if n := r.Write(in); n != 7 {
		t.Fatalf("write=%d", n)
	}
	out := make([]int16, 7)
	if n := r.Read(out); n != 7 {
		t.Fatalf("read=%d", n)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("wrap mismatch at %d: %d vs %d", i, in[i], out[i])
		}
	}
}

func TestFullThenWrite(t *testing.T) {
	r, _ := New(4)
	if n := r.Write([]int16{1, 2, 3, 4}); n != 4 {
		t.Fatalf("write=%d", n)
	}
	if n := r.Write([]int16{5, 6}); n != 0 {
		t.Fatalf("expected 0 when full, got %d", n)
	}
}

func TestSPSCConcurrent(t *testing.T) {
	r, _ := New(1024)
	const total = 1 << 20

	var produced uint64
	var consumed uint64

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := make([]int16, 128)
		var sent uint64
		for sent < total {
			n := min(total-sent, uint64(len(buf)))
			for i := range n {
				buf[i] = int16(sent + i)
			}
			for written := 0; written < int(n); {
				w := r.Write(buf[written:n])
				written += w
				if w == 0 {
					time.Sleep(time.Microsecond)
				}
			}
			sent += n
		}
		atomic.StoreUint64(&produced, sent)
	}()

	go func() {
		defer wg.Done()
		buf := make([]int16, 64)
		var got uint64
		for got < total {
			n := r.Read(buf)
			for i := range n {
				want := int16(got + uint64(i))
				if buf[i] != want {
					t.Errorf("seq mismatch at %d: got %d want %d", got+uint64(i), buf[i], want)
					return
				}
			}
			got += uint64(n)
			if n == 0 {
				time.Sleep(time.Microsecond)
			}
		}
		atomic.StoreUint64(&consumed, got)
	}()

	wg.Wait()
	if atomic.LoadUint64(&produced) != total || atomic.LoadUint64(&consumed) != total {
		t.Fatalf("counts: produced=%d consumed=%d total=%d",
			atomic.LoadUint64(&produced), atomic.LoadUint64(&consumed), total)
	}
}

func BenchmarkWriteRead(b *testing.B) {
	r, _ := New(4096)
	in := make([]int16, 480) // 30ms @ 16kHz
	out := make([]int16, 480)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Write(in)
		r.Read(out)
	}
}
