package models

import (
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"archive/tar"
)

var (
	errBadKind = errors.New("models: unknown entry kind")
)

// Progress is the callback fired during the download phase. done is
// bytes received so far; total is the announced Content-Length (may
// be -1 when the server omits it). Called from the installer
// goroutine — the caller must marshal UI updates onto the UI thread.
type Progress func(done, total int64)

// IsInstalled returns true when every file listed in RequiredFiles
// exists under InstallDir().
func (e Entry) IsInstalled() bool {
	dir, err := e.InstallDir()
	if err != nil {
		return false
	}
	for _, leaf := range e.RequiredFiles {
		if _, err := os.Stat(filepath.Join(dir, leaf)); err != nil {
			return false
		}
	}
	return true
}

// Install downloads + unpacks the entry into its canonical
// directory. Cancelling ctx aborts the in-flight HTTP request
// promptly. Progress fires roughly every 64 KiB of payload.
func (e Entry) Install(ctx context.Context, progress Progress) error {
	dir, err := e.InstallDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("models: mkdir %s: %w", dir, err)
	}

	resp, err := httpGet(ctx, e.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body := newProgressReader(resp.Body, resp.ContentLength, progress)

	if e.Tarball {
		return installTar(body, dir, e.Layout)
	}
	return installSingle(body, dir, e.Layout)
}

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models: http get %s: %w", url, err)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("models: http %d for %s", resp.StatusCode, url)
	}
	return resp, nil
}

// installSingle writes the body verbatim into <dir>/<dst>.
func installSingle(r io.Reader, dir string, layout map[string]string) error {
	dst, ok := layout[""]
	if !ok {
		return errors.New("models: single-file entry missing layout[\"\"]")
	}
	final := filepath.Join(dir, dst)
	tmp := final + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// installTar transparently unpacks bz2 / gz archives, picks only
// the members named in layout (or any descendants of a layout key
// that names a directory), and writes them under dir using the
// remapped leaf names. Anything not in layout is silently skipped.
func installTar(r io.Reader, dir string, layout map[string]string) error {
	br, err := autoDecompress(r)
	if err != nil {
		return err
	}
	defer maybeClose(br)

	tr := tar.NewReader(br)

	// Tarballs from k2-fsa wrap their contents in a single top-level
	// directory whose name we don't want to depend on; strip it.
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("models: tar read: %w", err)
		}
		name := stripFirstSegment(hdr.Name)
		if name == "" {
			continue
		}

		dst, ok := matchLayout(name, layout)
		if !ok {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := writeFile(filepath.Join(dir, dst), tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dir, dst), 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Skip — not relied on by any catalog entry.
		}
	}
	return nil
}

// matchLayout returns the destination path for an archive member, or
// ("", false) when the member is not selected. Both exact matches
// and prefix matches (when a layout key names a directory) are
// honoured.
func matchLayout(name string, layout map[string]string) (string, bool) {
	if dst, ok := layout[name]; ok {
		return dst, true
	}
	for src, dst := range layout {
		if !strings.HasSuffix(src, "/") && hasDirPrefix(name, src) {
			// e.g. layout key "espeak-ng-data" → match
			// "espeak-ng-data/ru_dict" → store at
			// <dir>/espeak-ng-data/ru_dict.
			rel := strings.TrimPrefix(name, src)
			rel = strings.TrimPrefix(rel, "/")
			return filepath.Join(dst, rel), true
		}
	}
	return "", false
}

// hasDirPrefix returns true iff name == dir or name starts with
// dir+"/".
func hasDirPrefix(name, dir string) bool {
	return name == dir || strings.HasPrefix(name, dir+"/")
}

func stripFirstSegment(p string) string {
	p = strings.TrimPrefix(p, "./")
	idx := strings.IndexByte(p, '/')
	if idx < 0 {
		// Top-level entry (the wrapping dir itself).
		return ""
	}
	return p[idx+1:]
}

func autoDecompress(r io.Reader) (io.Reader, error) {
	br, err := peekReader(r, 3)
	if err != nil {
		return nil, err
	}
	hdr := br.peek
	switch {
	case len(hdr) >= 3 && hdr[0] == 'B' && hdr[1] == 'Z' && hdr[2] == 'h':
		return bzip2.NewReader(br), nil
	case len(hdr) >= 2 && hdr[0] == 0x1f && hdr[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return gz, nil
	default:
		// Assume raw tar.
		return br, nil
	}
}

func maybeClose(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		_ = c.Close()
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func dirOf(p string) string { return filepath.Dir(p) }

// progressReader wraps an io.Reader and fires Progress every ~64 KiB.
type progressReader struct {
	r        io.Reader
	total    int64
	done     int64
	tick     int64
	progress Progress
}

func newProgressReader(r io.Reader, total int64, p Progress) *progressReader {
	return &progressReader{r: r, total: total, progress: p}
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.done += int64(n)
		p.tick += int64(n)
		const interval = 64 << 10
		if p.tick >= interval && p.progress != nil {
			p.tick = 0
			p.progress(p.done, p.total)
		}
	}
	if errors.Is(err, io.EOF) && p.progress != nil {
		p.progress(p.done, p.total)
	}
	return n, err
}

// peekReader buffers the first n bytes so we can detect bzip2 / gzip
// magic without losing them.
type peekedReader struct {
	peek []byte
	r    io.Reader
}

func peekReader(r io.Reader, n int) (*peekedReader, error) {
	buf := make([]byte, n)
	read := 0
	for read < n {
		k, err := r.Read(buf[read:])
		read += k
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return &peekedReader{peek: buf[:read], r: r}, nil
}

func (p *peekedReader) Read(b []byte) (int, error) {
	if len(p.peek) > 0 {
		n := copy(b, p.peek)
		p.peek = p.peek[n:]
		return n, nil
	}
	return p.r.Read(b)
}
