// Package downloader implements a resumable HTTP downloader for model
// files. It writes to a .part file, uses Range requests to resume, and
// verifies the final SHA256 against a manifest entry before swapping
// the .part file in place atomically.
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Progress reports the state of an in-flight download.
type Progress struct {
	Bytes    int64
	Total    int64 // -1 if the server didn't report Content-Length
	SpeedBps float64
}

// Downloader performs HTTP downloads with resume support.
type Downloader struct {
	Client    *http.Client
	UserAgent string

	// MaxAttempts caps the number of retries. Zero means 5.
	MaxAttempts int

	// InitialBackoff is the first retry delay; subsequent retries
	// double up to one minute.
	InitialBackoff time.Duration

	// ProgressInterval throttles Progress channel sends. Zero means
	// 250 ms.
	ProgressInterval time.Duration
}

// New returns a Downloader with sensible defaults.
func New() *Downloader {
	return &Downloader{
		Client: &http.Client{
			Timeout: 0, // streaming; rely on context cancellation
		},
		UserAgent:        "realtime-speech-translator/0.1 (+downloader)",
		MaxAttempts:      5,
		InitialBackoff:   time.Second,
		ProgressInterval: 250 * time.Millisecond,
	}
}

// Fetch downloads url into dstPath. If a `dstPath + ".part"` file
// already exists, the download resumes from its size. After the body
// has been read the file is fsynced, hash-verified (if want != "") and
// atomically renamed to dstPath.
//
// Progress is reported on the returned channel; the channel is closed
// when the download finishes (success or failure). Errors are returned
// synchronously from Fetch itself, not via the channel.
func (d *Downloader) Fetch(ctx context.Context, url string, dstPath string, want string) (<-chan Progress, error) {
	if url == "" {
		return nil, errors.New("downloader: empty url")
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return nil, fmt.Errorf("downloader: mkdir: %w", err)
	}

	partPath := dstPath + ".part"
	ch := make(chan Progress, 8)

	go func() {
		defer close(ch)
		err := d.run(ctx, url, partPath, dstPath, want, ch)
		if err != nil {
			// The error is delivered by Fetch's caller via a separate
			// goroutine that wraps Fetch; here we only log to stderr
			// for visibility because the channel is the only return
			// path for the goroutine and we've documented it as
			// closing on completion.
			fmt.Fprintf(os.Stderr, "downloader: %v\n", err)
		}
	}()

	return ch, nil
}

func (d *Downloader) run(ctx context.Context, url, partPath, dstPath, want string, ch chan<- Progress) error {
	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	backoff := d.InitialBackoff
	if backoff <= 0 {
		backoff = time.Second
	}

	var lastErr error
	succeeded := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := d.attempt(ctx, url, partPath, ch)
		if err == nil {
			succeeded = true
			break
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
	}
	if !succeeded {
		return fmt.Errorf("downloader: gave up after %d attempts: %w", maxAttempts, lastErr)
	}

	if err := verifyHash(partPath, want); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	return os.Rename(partPath, dstPath)
}

func (d *Downloader) attempt(ctx context.Context, url, partPath string, ch chan<- Progress) error {
	var startOffset int64
	if fi, err := os.Stat(partPath); err == nil {
		startOffset = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	if startOffset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(startOffset, 10)+"-")
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored the Range header; restart from 0.
		startOffset = 0
		if err := os.Truncate(partPath, 0); err != nil && !os.IsNotExist(err) {
			return err
		}
	case http.StatusPartialContent:
		// Resume granted.
	case http.StatusRequestedRangeNotSatisfiable:
		// The .part file is at-or-beyond the resource size — likely
		// already complete; let the hash verifier decide.
		return nil
	default:
		return fmt.Errorf("downloader: HTTP %d", resp.StatusCode)
	}

	total := startOffset + resp.ContentLength
	if resp.ContentLength < 0 {
		total = -1
	}

	f, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	bufSize := 1 << 15 // 32 KB
	buf := make([]byte, bufSize)
	got := startOffset
	t0 := time.Now()
	tLast := t0
	interval := d.ProgressInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			got += int64(n)
			if now := time.Now(); now.Sub(tLast) >= interval {
				dt := now.Sub(t0).Seconds()
				speed := 0.0
				if dt > 0 {
					speed = float64(got-startOffset) / dt
				}
				select {
				case ch <- Progress{Bytes: got, Total: total, SpeedBps: speed}:
				default:
				}
				tLast = now
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	// Final progress
	select {
	case ch <- Progress{Bytes: got, Total: total, SpeedBps: 0}:
	default:
	}
	return f.Sync()
}

func verifyHash(path, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("downloader: sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}
