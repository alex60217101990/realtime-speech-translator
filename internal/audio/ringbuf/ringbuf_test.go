package ringbuf

import (
	"math/rand"
	"sync"
	"testing"
)

func TestNextPow2(t *testing.T) {
	cases := map[int]int{0: 1, 1: 1, 2: 2, 3: 4, 5: 8, 64: 64, 65: 128, 1023: 1024, 1024: 1024}
	for in, want := range cases {
		if got := nextPow2(in); got != want {
			t.Errorf("nextPow2(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCapRoundsUp(t *testing.T) {
	r := New(100)
	if r.Cap() != 128 {
		t.Errorf("Cap() = %d, want 128 (next pow2 >= 100)", r.Cap())
	}
}

func TestWriteRead_RoundTrip(t *testing.T) {
	r := New(16)
	in := []float32{1, 2, 3, 4, 5}
	if n := r.Write(in); n != 5 {
		t.Fatalf("Write returned %d, want 5", n)
	}
	if r.Len() != 5 {
		t.Fatalf("Len = %d, want 5", r.Len())
	}
	out := make([]float32, 5)
	if n := r.Read(out); n != 5 {
		t.Fatalf("Read returned %d, want 5", n)
	}
	for i, v := range in {
		if out[i] != v {
			t.Errorf("out[%d] = %v, want %v", i, out[i], v)
		}
	}
	if r.Len() != 0 {
		t.Errorf("Len after read = %d, want 0", r.Len())
	}
}

func TestWrap(t *testing.T) {
	r := New(8) // cap 8 → mask 7
	// Fill almost full, drain part, refill — forces wraparound.
	first := []float32{1, 2, 3, 4, 5, 6}
	if n := r.Write(first); n != 6 {
		t.Fatalf("Write1 returned %d", n)
	}
	dst := make([]float32, 4)
	if n := r.Read(dst); n != 4 {
		t.Fatalf("Read1 returned %d", n)
	}
	second := []float32{7, 8, 9, 10, 11, 12}
	if n := r.Write(second); n != 6 {
		t.Fatalf("Write2 returned %d (expected 6, cap-Len=8-2=6)", n)
	}
	all := make([]float32, 8)
	if n := r.Read(all); n != 8 {
		t.Fatalf("Read2 returned %d, want 8", n)
	}
	want := []float32{5, 6, 7, 8, 9, 10, 11, 12}
	for i, v := range want {
		if all[i] != v {
			t.Errorf("all[%d] = %v, want %v", i, all[i], v)
		}
	}
}

func TestWriteShort_WhenFull(t *testing.T) {
	r := New(4)
	full := []float32{1, 2, 3, 4}
	r.Write(full)
	extra := []float32{5, 6, 7}
	if n := r.Write(extra); n != 0 {
		t.Errorf("Write into full ring returned %d, want 0", n)
	}
}

func TestReadShort_WhenEmpty(t *testing.T) {
	r := New(4)
	dst := make([]float32, 4)
	if n := r.Read(dst); n != 0 {
		t.Errorf("Read from empty ring returned %d, want 0", n)
	}
}

// TestSPSC_Concurrent stresses the producer/consumer lock-free
// contract: a sequence written by one goroutine must be read in
// order by the other, with no duplication or loss.
func TestSPSC_Concurrent(t *testing.T) {
	const total = 1 << 18
	r := New(1024)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(99))
		written := 0
		for written < total {
			batch := 1 + rng.Intn(64)
			if written+batch > total {
				batch = total - written
			}
			buf := make([]float32, batch)
			for i := range buf {
				buf[i] = float32(written + i)
			}
			for {
				n := r.Write(buf)
				if n == int(batch) {
					break
				}
				if n > 0 {
					buf = buf[n:]
					batch -= n
				}
			}
			written += batch
		}
	}()

	go func() {
		defer wg.Done()
		next := float32(0)
		rng := rand.New(rand.NewSource(42))
		read := 0
		dst := make([]float32, 64)
		for read < total {
			want := 1 + rng.Intn(64)
			if read+want > total {
				want = total - read
			}
			n := r.Read(dst[:want])
			for i := 0; i < n; i++ {
				if dst[i] != next {
					t.Errorf("at pos %d: got %v, want %v", read+i, dst[i], next)
					return
				}
				next++
			}
			read += n
		}
	}()

	wg.Wait()
}

func BenchmarkWriteRead_PowerOfTwo(b *testing.B) {
	r := New(2048)
	in := make([]float32, 256)
	out := make([]float32, 256)
	b.SetBytes(int64(len(in) * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Write(in)
		r.Read(out)
	}
}
