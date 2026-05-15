// Package crashreport captures Go panics into local report files.
// Reports stay on disk under the application data directory; nothing
// is sent over the network. The UI exposes a "Send crash report"
// affordance for the user to upload manually (M8 — for now the dir
// is the user's clipboard target).
//
// Layout:
//
//	<data>/crash-reports/<timestamp>.txt
//
// Each file contains:
//
//	build         git SHA / version
//	when          ISO-8601 UTC
//	goroutine     id of the panicking goroutine
//	stack         full debug.Stack() output
//	state         optional caller-supplied context (Session state etc.)
package crashreport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/models/paths"
)

// Report is one written-to-disk crash record.
type Report struct {
	Path    string
	When    time.Time
	Stack   string
	State   string
	Version string
}

// Dir returns the directory crash reports live in, creating it if
// necessary.
func Dir() (string, error) {
	d, err := paths.Data()
	if err != nil {
		return "", err
	}
	out := filepath.Join(d, "crash-reports")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

// Save persists a crash record. version may be empty (best-effort);
// state is a free-form string the host fills in (e.g. session state
// at the moment of crash).
//
// Returns the path of the written file. Errors propagate to the caller
// — the panic recover wrapper logs them via slog.
func Save(version, state, stack string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	// Nanosecond precision avoids name collisions when crashes happen
	// in quick succession (panic loop, tests, racing goroutines).
	name := fmt.Sprintf("%s.txt", now.Format("20060102-150405.000000000Z"))
	path := filepath.Join(dir, name)

	body := fmt.Sprintf(
		"build:   %s\nwhen:    %s\nstate:   %s\n----- stack -----\n%s\n",
		nonEmpty(version, "(unknown)"),
		now.Format(time.RFC3339),
		nonEmpty(state, "-"),
		stack,
	)
	tmp := path + ".part"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// Recover is meant for `defer crashreport.Recover(version, state, onSave)`.
// It rethrows after writing the report so the program still aborts and
// the OS / supervisor can react — Recover is not "swallow", it's
// "record + rethrow".
//
// onSave is optional; when non-nil it is invoked with the report path
// (or an empty string if Save failed) so the host can surface a
// "Crash report written to: …" dialog before re-raising.
func Recover(version string, state func() string, onSave func(path string, err error)) {
	r := recover()
	if r == nil {
		return
	}
	stk := string(debug.Stack())
	stateStr := ""
	if state != nil {
		// state() may itself panic if it reads racy session pointers;
		// guard so we still write a report.
		func() {
			defer func() { _ = recover() }()
			stateStr = state()
		}()
	}
	path, err := Save(version, stateStr, fmt.Sprintf("panic: %v\n\n%s", r, stk))
	if onSave != nil {
		onSave(path, err)
	}
	// rethrow to preserve original behaviour (process exits non-zero
	// + stderr stack from runtime).
	panic(r)
}

// List enumerates reports in chronological order (newest first).
func List() ([]Report, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Report, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Report{
			Path: filepath.Join(dir, e.Name()),
			When: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out, nil
}

// Prune trims the report directory to keep at most `keep` newest
// files. Useful from app startup to avoid unbounded growth on a
// machine that crashes nightly.
func Prune(keep int) error {
	if keep < 0 {
		return errors.New("crashreport: negative keep")
	}
	list, err := List()
	if err != nil {
		return err
	}
	if len(list) <= keep {
		return nil
	}
	for _, r := range list[keep:] {
		_ = os.Remove(r.Path)
	}
	return nil
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// installMu guards Install / Uninstall against concurrent setup.
var installMu sync.Mutex
