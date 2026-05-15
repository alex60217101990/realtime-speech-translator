package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	sha256    string
	localPath string
	// extras are auxiliary files fetched sequentially after the
	// primary `url`. Each entry downloads without a checksum (the
	// upstream HF mirrors do not publish per-file SHA256). Used by MT
	// rows (config.json + sentencepiece.model) and TTS rows (.onnx.json).
	extras []extraFile
}

type extraFile struct {
	url       string
	localPath string
}

// installed reports whether every required file exists on disk.
func (r modelRow) installed() bool {
	if !paths.Exists(r.localPath) {
		return false
	}
	for _, e := range r.extras {
		if !paths.Exists(e.localPath) {
			return false
		}
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
		dir, _ := paths.MTDir(name)
		extras := []extraFile{}
		if mt.ConfigURL != "" {
			extras = append(extras, extraFile{
				url:       mt.ConfigURL,
				localPath: filepath.Join(dir, "config.json"),
			})
		}
		if mt.TokenizerURL != "" {
			extras = append(extras, extraFile{
				url:       mt.TokenizerURL,
				localPath: filepath.Join(dir, "sentencepiece.model"),
			})
		}
		rows = append(rows, modelRow{
			id:        name,
			kind:      "mt",
			label:     "MT " + name,
			sizeMB:    mt.SizeMB,
			url:       mt.URL,
			sha256:    mt.SHA256,
			// model.bin is the canonical "installed" marker; extras
			// (config.json, sentencepiece.model) are checked alongside.
			localPath: filepath.Join(dir, "model.bin"),
			extras:    extras,
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
			localPath: p,
			extras:    []extraFile{{url: v.JSONURL, localPath: j}},
		})
	}
	return rows
}

// ModelsScreen builds a Fyne widget that lists every model in the
// embedded manifest and lets the user download missing ones.
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
	for _, e := range r.extras {
		size += paths.FileSize(e.localPath)
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
		for _, e := range r.extras {
			bytes += paths.FileSize(e.localPath)
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
	for p := range ch {
		updateProgress(bar, status, p)
	}
	// Fetch closes ch when the goroutine writing to disk finishes; we
	// can immediately proceed to extras here.
	for _, e := range r.extras {
		if e.url == "" || e.localPath == "" {
			continue
		}
		ech, err := d.Fetch(ctx, e.url, e.localPath, "")
		if err != nil {
			return err
		}
		for p := range ech {
			updateProgress(bar, status, p)
		}
	}
	// Final sanity check: primary file landed.
	if !paths.Exists(r.localPath) || paths.FileSize(r.localPath) == 0 {
		return fmt.Errorf("download finished but %s is missing", r.localPath)
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

// silence unused-import lint when path build tags pluck out time/os.
var (
	_ = os.PathSeparator
	_ = time.Now
)
