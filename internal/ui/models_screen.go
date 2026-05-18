package ui

import (
	"context"
	"fmt"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/alex60217101990/realtime-speech-translator/internal/models"
)

// ModelsCallbacks lets the host react to install completion. The
// translator typically refreshes the Settings dropdowns and warm-
// reloads the engines on completion of a relevant kind.
type ModelsCallbacks struct {
	// OnInstalled is fired once per entry after a successful
	// download + extract. Called from the Fyne goroutine; it is
	// safe to touch widgets from inside.
	OnInstalled func(models.Entry)
}

// ModelsScreen renders one row per catalog entry: name, kind,
// expected size, installed-status badge, install / reinstall button
// and a per-row progress bar.
func ModelsScreen(w fyne.Window, catalog []models.Entry, cb ModelsCallbacks) fyne.CanvasObject {
	rows := container.NewVBox(
		widget.NewLabelWithStyle("Models", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel(""+
			"Pick the artefacts you want and click Install. Files land in the "+
			"per-OS data directory and are picked up by the engines on the next "+
			"session restart."),
	)

	for _, e := range catalog {
		rows.Add(buildModelRow(w, e, cb))
		rows.Add(widget.NewSeparator())
	}

	return container.NewVScroll(rows)
}

// buildModelRow constructs one composite row widget. Each row owns
// its own cancel context so the user can cancel a download via the
// button.
func buildModelRow(w fyne.Window, entry models.Entry, cb ModelsCallbacks) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(
		fmt.Sprintf("%s   [%s]  %s", entry.DisplayName, entry.Kind, sizeHuman(entry.SizeBytes)),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true},
	)
	license := widget.NewLabel(entry.License)

	status := widget.NewLabel("")
	prog := widget.NewProgressBar()
	prog.Min, prog.Max = 0, 1
	prog.Hide()

	var (
		mu       sync.Mutex
		cancel   context.CancelFunc
		btn      *widget.Button
		updateUI = func() {
			if entry.IsInstalled() {
				status.SetText("✓ installed")
				btn.SetText("Reinstall")
			} else {
				status.SetText("not installed")
				btn.SetText("Install")
			}
		}
	)

	btn = widget.NewButton("Install", func() {
		mu.Lock()
		if cancel != nil {
			// User clicked while a download is running → treat as
			// cancel.
			cancel()
			cancel = nil
			mu.Unlock()
			return
		}
		ctx, cancelFn := context.WithCancel(context.Background())
		cancel = cancelFn
		mu.Unlock()

		btn.SetText("Cancel")
		fyne.Do(func() {
			prog.SetValue(0)
			prog.Show()
		})

		go func() {
			err := entry.Install(ctx, func(done, total int64) {
				v := 0.0
				if total > 0 {
					v = float64(done) / float64(total)
				}
				fyne.Do(func() {
					prog.SetValue(v)
					status.SetText(fmt.Sprintf("%s / %s", sizeHuman(done), sizeHuman(total)))
				})
			})
			mu.Lock()
			cancel = nil
			mu.Unlock()

			fyne.Do(func() {
				prog.Hide()
				if err != nil {
					status.SetText("error: " + err.Error())
					btn.SetText("Retry")
					dialog.ShowError(err, w)
					return
				}
				updateUI()
				if cb.OnInstalled != nil {
					cb.OnInstalled(entry)
				}
			})
		}()
	})

	updateUI()

	header := container.NewBorder(nil, nil, nil, btn, title)
	body := container.NewVBox(license, status, prog)
	return container.NewVBox(header, body)
}

// sizeHuman renders a byte count as "1.5 MB" / "640 KB" / "—".
func sizeHuman(n int64) string {
	if n <= 0 {
		return "—"
	}
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
