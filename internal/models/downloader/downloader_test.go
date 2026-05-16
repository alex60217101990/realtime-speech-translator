package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sha256Hex returns the lower-case hex sha256 of payload.
func sha256Hex(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

func TestFetchSuccess(t *testing.T) {
	payload := []byte(strings.Repeat("abcdefgh", 1024)) // 8 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "model.bin")

	d := New()
	d.InitialBackoff = 10 * time.Millisecond
	ch, err := d.Fetch(context.Background(), srv.URL, dst, sha256Hex(payload))
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("payload mismatch")
	}
}

func TestFetchResume(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 4096)) // 40 KB
	rangeReqs := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rng := r.Header.Get("Range"); rng != "" {
			rangeReqs++
			start := int64(0)
			fmt.Sscanf(rng, "bytes=%d-", &start)
			w.Header().Set("Content-Range",
				fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-int(start)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "model.bin")
	part := dst + ".part"

	// Seed a .part file with the first 10 KB so the downloader has to
	// resume.
	if err := os.WriteFile(part, payload[:10000], 0o644); err != nil {
		t.Fatal(err)
	}

	d := New()
	d.InitialBackoff = 10 * time.Millisecond
	ch, err := d.Fetch(context.Background(), srv.URL, dst, sha256Hex(payload))
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if rangeReqs == 0 {
		t.Fatal("expected at least one Range request")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch (len got=%d want=%d)", len(got), len(payload))
	}
}

func TestFetchRetryThenSucceed(t *testing.T) {
	payload := []byte("hello-world-payload")
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		if tries == 1 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "model.bin")

	d := New()
	d.InitialBackoff = 10 * time.Millisecond
	d.MaxAttempts = 3
	ch, err := d.Fetch(context.Background(), srv.URL, dst, sha256Hex(payload))
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if tries < 2 {
		t.Fatalf("expected >=2 tries, got %d", tries)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(payload) {
		t.Fatal("payload mismatch")
	}
}

func TestFetchSHA256Mismatch(t *testing.T) {
	payload := []byte("real-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "model.bin")

	d := New()
	d.InitialBackoff = 10 * time.Millisecond
	ch, err := d.Fetch(context.Background(), srv.URL, dst, sha256Hex([]byte("not-real")))
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("dst should not exist on hash mismatch")
	}
}

func TestFetchContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		// Slow drip — never finishes within the cancel window.
		buf := make([]byte, 1024)
		for range 1024 {
			if _, err := w.Write(buf); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "model.bin")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	d := New()
	d.InitialBackoff = 10 * time.Millisecond
	d.MaxAttempts = 1
	ch, err := d.Fetch(ctx, srv.URL, dst, "")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	// Either nothing was written or the .part file was left behind —
	// both are acceptable. The contract is that the final dst does
	// not exist when the context is cancelled mid-flight.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("dst should not exist after cancel")
	}
}

// silence unused-import warnings for io when only one helper is needed.
var _ = io.Copy
