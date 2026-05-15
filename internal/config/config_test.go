package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withTempHome redirects HOME / XDG paths to a temp dir so Path() lands
// in a writable directory we own.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(dir, "AppData", "Local"))
	}
	return dir
}

func TestDefault(t *testing.T) {
	s := Default()
	if s.SourceLang == "" || s.TargetLang == "" {
		t.Fatal("default lang fields empty")
	}
	if s.WhisperModel != "small" {
		t.Fatalf("expected small, got %q", s.WhisperModel)
	}
	if s.Theme != "system" {
		t.Fatalf("expected system theme, got %q", s.Theme)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	withTempHome(t)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != Default() {
		t.Fatalf("expected Default, got %+v", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempHome(t)
	want := Default()
	want.SourceLang = "ru"
	want.TargetLang = "en"
	want.Threads = 4
	want.VADAggressiveness = 3
	want.OutputDevice = "BlackHole"
	want.Theme = "dark"
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	withTempHome(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("source_lang: en\nfuture_field: someval\nwhisper_model: tiny\n")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("expected to ignore unknown fields, got %v", err)
	}
	if got.SourceLang != "en" || got.WhisperModel != "tiny" {
		t.Fatalf("partial load failed: %+v", got)
	}
}
