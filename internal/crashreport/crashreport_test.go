package crashreport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func withTempData(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(dir, "AppData", "Local"))
	}
}

func TestSaveAndList(t *testing.T) {
	withTempData(t)

	p1, err := Save("v1.2.3", "running", "boom #1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Save("v1.2.3", "starting", "boom #2")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{p1, p2} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "build:   v1.2.3") {
			t.Errorf("missing build line in %s", p)
		}
		if !strings.Contains(s, "----- stack -----") {
			t.Errorf("missing stack separator in %s", p)
		}
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(list))
	}
}

func TestPrune(t *testing.T) {
	withTempData(t)
	for range 5 {
		if _, err := Save("v0", "s", "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prune(2); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 after prune, got %d", len(list))
	}
}

func TestRecoverRethrows(t *testing.T) {
	withTempData(t)
	gotPath := ""
	gotErr := error(nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected re-thrown panic")
		}
		if gotPath == "" {
			t.Fatalf("expected report path captured, gotErr=%v", gotErr)
		}
		if !strings.HasSuffix(gotPath, ".txt") {
			t.Fatalf("unexpected path: %s", gotPath)
		}
	}()
	defer Recover("v-test",
		func() string { return "test-state" },
		func(path string, err error) {
			gotPath, gotErr = path, err
		})
	panic("boom from test")
}
