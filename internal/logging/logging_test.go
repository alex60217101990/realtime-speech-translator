package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.log")
	closeFn, err := Init(Options{FilePath: fp, Level: slog.LevelInfo, LevelExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("hello", "k", 1)
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"msg":"hello"`) {
		t.Fatalf("missing message; got %q", string(b))
	}
}

func TestRotateIfTooBig(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "rot.log")
	if err := os.WriteFile(fp, make([]byte, MaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateIfTooBig(fp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatal("expected primary file to be rotated away")
	}
	if _, err := os.Stat(fp + ".1"); err != nil {
		t.Fatal("expected .1 to exist")
	}
}

func TestRotateSkipSmall(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "small.log")
	if err := os.WriteFile(fp, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateIfTooBig(fp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); err != nil {
		t.Fatal("expected primary file to be left in place")
	}
}

func TestRotateMissing(t *testing.T) {
	if err := rotateIfTooBig(filepath.Join(t.TempDir(), "nope.log")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLevelEnv(t *testing.T) {
	cases := []struct {
		env  string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"chatty", slog.LevelInfo},
	}
	for _, c := range cases {
		t.Setenv("LOG_LEVEL", c.env)
		got := resolveLevel(Options{})
		if got != c.want {
			t.Errorf("LOG_LEVEL=%q: got %v want %v", c.env, got, c.want)
		}
	}
}
