package ui

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/alex60217101990/realtime-speech-translator/internal/models/downloader"
	"github.com/alex60217101990/realtime-speech-translator/internal/models/manifest"
	"github.com/alex60217101990/realtime-speech-translator/internal/models/paths"
)

// modelRow describes a single row in the Models Manager list.
type modelRow struct {
	id        string
	kind      string // "whisper" / "tts" / "mt"
	label     string
	sizeMB    int
	url       string
	jsonURL   string // empty for non-TTS
	sha256    string
	localPath string
	jsonLocal string // empty for non-TTS
}

// installed reports whether the on-disk file exists with non-zero size.
func (r modelRow) installed() bool {
	if !paths.Exists(r.localPath) {
		return false
	}
	if r.jsonLocal != "" && !paths.Exists(r.jsonLocal) {
		return false
	}
	return true
}

// gatherRows builds the model row list from a parsed manifest.
func gatherRows(m *manifest.File) []modelRow {
	rows := make([]modelRow, 0, len(m.Whisper)+len(m.MT)+len(m.TTS))
	for name, w := range m.Whisper {
		p, _ := paths.Whisper(name)
		rows = append(rows, modelRow{
			id:        name,
			kind:      "whisper",
			label:     "Whisper " + name,
			sizeMB:    w.SizeMB,
			url:       w.URL,
			sha256:    w.SHA256,
			localPath: p,
		})
	}
	for name, mt := range m.MT {
		p, _ := paths.MTDir(name)
		rows = append(rows, modelRow{
			id:        name,
			kind:      "mt",
			label:     "MT " + name,
			sizeMB:    mt.SizeMB,
			url:       mt.URL,
			sha256:    mt.SHA256,
			// model.bin is the canonical "installed" marker
			localPath: p + string(os.PathSeparator) + "model.bin",
		})
	}
	for name, v := range m.TTS {
		p, _ := paths.TTSVoice(name)
		j, _ := paths.TTSVoiceJSON(name)
		rows = append(rows, modelRow{
			id:        name,
			kind:      "tts",
			label:     "TTS " + v.Lang + " · " + name,
			sizeMB:    v.SizeMB,
			url:       v.ONNXURL,
			jsonURL:   v.JSONURL,
			localPath: p,
			jsonLocal: j,
		})
	}
	return rows
}

// ModelsScreen builds a Fyne widget that lists every model in the
// embedded manifest and lets the user download missing ones. Existing
// files are flagged with size; downloads run on a background goroutine
// per row.
func ModelsScreen(w fyne.Window) fyne.CanvasObject {
	mf, err := manifest.Load()
	if err != nil {
		return widget.NewLabel("manifest load failed: " + err.Error())
	}
	rows := gatherRows(mf)

	dlClient := downloader.New()
	header := widget.NewLabelWithStyle("Models", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	listBox := container.NewVBox()
	var mu sync.Mutex
	progressByRow := make(map[string]*widget.ProgressBar)
	statusByRow := make(map[string]*widget.Label)

	for _, r := range rows {
		row := r // capture
		statusLbl := widget.NewLabel(rowStatus(row))
		progress := widget.NewProgressBar()
		progress.Hide()
		var btn *widget.Button
		btn = widget.NewButton(rowAction(row), func() {
			if row.installed() {
				return // delete-flow lives in M6
			}
			btn.Disable()
			progress.Show()
			go func() {
				err := downloadRow(context.Background(), dlClient, row, progress, statusLbl)
				// All widget mutations must happen on the Fyne goroutine
				// in v2.7+. Bindings are safe; raw widget setters are
				// not.
				fyne.Do(func() {
					if err != nil {
						statusLbl.SetText("error: " + err.Error())
					} else {
						statusLbl.SetText(rowStatus(row))
						btn.SetText(rowAction(row))
					}
					progress.Hide()
					btn.Enable()
				})
			}()
		})

		mu.Lock()
		progressByRow[row.id] = progress
		statusByRow[row.id] = statusLbl
		mu.Unlock()

		listBox.Add(container.NewBorder(
			nil, nil,
			widget.NewLabel(row.label),
			btn,
			container.NewVBox(statusLbl, progress),
		))
	}

	scroll := container.NewVScroll(listBox)
	scroll.SetMinSize(fyne.NewSize(720, 360))

	disk := widget.NewLabel(diskUsageSummary(rows))
	refresh := widget.NewButton("Refresh", func() {
		disk.SetText(diskUsageSummary(rows))
		for _, r := range rows {
			if lbl, ok := statusByRow[r.id]; ok {
				lbl.SetText(rowStatus(r))
			}
		}
	})

	return container.NewBorder(
		container.NewHBox(header, refresh),
		disk,
		nil, nil,
		scroll,
	)
}

func rowStatus(r modelRow) string {
	if !r.installed() {
		return fmt.Sprintf("missing (%d MB)", r.sizeMB)
	}
	size := paths.FileSize(r.localPath)
	if r.jsonLocal != "" {
		size += paths.FileSize(r.jsonLocal)
	}
	return fmt.Sprintf("installed (%d MB)", size>>20)
}

func rowAction(r modelRow) string {
	if r.installed() {
		return "Re-download"
	}
	return "Download"
}

func diskUsageSummary(rows []modelRow) string {
	var bytes int64
	for _, r := range rows {
		if !r.installed() {
			continue
		}
		bytes += paths.FileSize(r.localPath)
		if r.jsonLocal != "" {
			bytes += paths.FileSize(r.jsonLocal)
		}
	}
	return fmt.Sprintf("Disk usage: %d MB", bytes>>20)
}

func downloadRow(ctx context.Context, d *downloader.Downloader, r modelRow, bar *widget.ProgressBar, status *widget.Label) error {
	if r.url == "" {
		return fmt.Errorf("no URL")
	}
	ch, err := d.Fetch(ctx, r.url, r.localPath, r.sha256)
	if err != nil {
		return err
	}
	go func() {
		for p := range ch {
			updateProgress(bar, status, p)
		}
	}()
	if r.jsonURL != "" {
		// JSON sidecar — small, sequential is fine.
		time.Sleep(50 * time.Millisecond)
		jch, err := d.Fetch(ctx, r.jsonURL, r.jsonLocal, "")
		if err != nil {
			return err
		}
		for range jch {
		}
	}
	// Wait for primary file to fully arrive.
	for {
		if paths.Exists(r.localPath) && paths.FileSize(r.localPath) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func updateProgress(bar *widget.ProgressBar, status *widget.Label, p downloader.Progress) {
	// Widget setters from a background goroutine; wrap so Fyne can
	// dispatch them on its main loop. The download pump fires several
	// times per second — fyne.Do is non-blocking and safe here.
	fyne.Do(func() {
		if p.Total > 0 {
			bar.SetValue(float64(p.Bytes) / float64(p.Total))
		}
		mb := p.Bytes >> 20
		if p.Total > 0 {
			status.SetText(fmt.Sprintf("downloading: %d / %d MB", mb, p.Total>>20))
		} else {
			status.SetText(fmt.Sprintf("downloading: %d MB", mb))
		}
	})
}
