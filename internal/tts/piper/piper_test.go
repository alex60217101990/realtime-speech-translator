package piper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakePiper creates an executable script at dir/piper that, on
// invocation, emits a small deterministic PCM blob to stdout so the
// wrapper can be exercised without the real binary.
func writeFakePiper(t *testing.T, dir string) string {
	t.Helper()
	var script, ext string
	switch runtime.GOOS {
	case "windows":
		ext = ".bat"
		script = "@echo off\r\nset /p _x=\r\n"
	default:
		ext = ""
		// Echo a 16-byte LE int16 sequence: [100, 200, 300, 400, 500, 600, 700, 800].
		// Use printf with octal escapes so we don't depend on python/perl.
		script = `#!/bin/sh
read line
printf '\144\0\310\0\54\1\220\1\364\1\130\2\274\2\40\3'
`
	}
	path := filepath.Join(dir, "fakepiper"+ext)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewBinaryNotFound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BinaryPath = "definitely-not-on-path-12345"
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestSynthesizeEmptyText(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakePiper(t, dir)
	e, err := New(Config{BinaryPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddVoice(Voice{Lang: "en", ONNXPath: filepath.Join(dir, "voice.onnx")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Synthesize(context.Background(), "   ", "en"); err == nil {
		t.Fatal("expected empty-text error")
	}
}

func TestSynthesizeNoVoice(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakePiper(t, dir)
	e, err := New(Config{BinaryPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Synthesize(context.Background(), "hello", "en"); err == nil {
		t.Fatal("expected no-voice error")
	}
}

func TestSynthesizeProducesPCM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake piper script is POSIX-only; cgo build matrix covers windows separately")
	}
	dir := t.TempDir()
	bin := writeFakePiper(t, dir)
	e, err := New(Config{BinaryPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddVoice(Voice{Lang: "en", ONNXPath: filepath.Join(dir, "voice.onnx")}); err != nil {
		t.Fatal(err)
	}
	pcm, err := e.Synthesize(context.Background(), "hello", "en")
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{100, 200, 300, 400, 500, 600, 700, 800}
	if len(pcm) != len(want) {
		t.Fatalf("len=%d want %d", len(pcm), len(want))
	}
	for i, v := range want {
		if pcm[i] != v {
			t.Fatalf("idx %d: got %d want %d", i, pcm[i], v)
		}
	}
}

func TestSynthesizeBinaryFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only failing script")
	}
	dir := t.TempDir()
	failing := filepath.Join(dir, "fail.sh")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{BinaryPath: failing})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddVoice(Voice{Lang: "en", ONNXPath: filepath.Join(dir, "v.onnx")}); err != nil {
		t.Fatal(err)
	}
	_, err = e.Synthesize(context.Background(), "hello", "en")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exit error, got %v", err)
	}
}

func TestBytesToInt16Endianness(t *testing.T) {
	// Verify our reinterpretation matches little-endian decoding.
	src := []int16{0, 1, -1, 32767, -32768}
	var buf bytes.Buffer
	for _, v := range src {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	got := bytesToInt16(buf.Bytes())
	if len(got) != len(src) {
		t.Fatalf("len=%d", len(got))
	}
	for i, v := range src {
		if got[i] != v {
			t.Fatalf("idx %d: got %d want %d", i, got[i], v)
		}
	}
}
