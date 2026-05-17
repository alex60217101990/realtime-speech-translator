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

// ModelsCallbacks lets the parent UI react to actions inside the
// Models tab. All callbacks are optional.
type ModelsCallbacks struct {
	// OnMTInstalled fires after an MT row's primary archive (or
	// model.bin + extras) finished downloading and verifying. Backend
	// is the manifest backend tag ("madlad" / "m2m100" / "small100" /
	// "opusmt"); the parent uses it to decide whether to hot-swap the
	// running translator. Called from a goroutine — wrap UI work in
	// fyne.Do.
	OnMTInstalled func(backend string)
	// OnPiperInstalled fires after the auto-install flow for the
	// upstream piper binary completes. The parent re-resolves the TTS
	// engine path and rebuilds piper.Engine so playback starts working
	// without an app restart. Called from a goroutine.
	OnPiperInstalled func()
}

// modelRow describes a single row in the Models Manager list.
type modelRow struct {
	id        string
	kind      string // "whisper" / "tts" / "mt"
	backend   string // manifest backend tag, empty for non-MT rows
	label     string
	sizeMB    int
	tier      manifest.Tier
	url       string
	sha256    string
	localPath string
	// extras are auxiliary files fetched sequentially after the
	// primary `url`. Each entry downloads without a checksum (the
	// upstream HF mirrors do not publish per-file SHA256). Used by MT
	// rows (config.json + sentencepiece.model) and TTS rows (.onnx.json).
	extras []extraFile

	// manual marks entries the application can't download automatically
	// — typically because the upstream CT2 mirror is gated or missing.
	// The UI replaces the Download button with a disabled "Manual
	// install" hint pointing at the README.
	manual bool

	// archive switches the download flow into "fetch one tarball,
	// unpack into the model directory" mode. archiveDir is the
	// extraction target; localPath is recomputed as
	// archiveDir + "/model.bin" so the existing installed-check works.
	archive    bool
	archiveDir string
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
			tier:      w.Tier,
			url:       w.URL,
			sha256:    w.SHA256,
			localPath: p,
		})
	}
	for name, mt := range m.MT {
		// OPUS-MT models live under mt/opusmt/<pair>/ so the loader can
		// enumerate every downloaded pair from a single root. MADLAD /
		// m2m100 keep the legacy mt/<name>/ layout.
		var dir string
		if mt.Backend == "opusmt" && mt.Pair != "" {
			dir, _ = paths.OPUSMTPair(mt.Pair)
		} else {
			dir, _ = paths.MTDir(name)
		}
		extras := []extraFile{}
		if mt.ConfigURL != "" {
			extras = append(extras, extraFile{
				url:       mt.ConfigURL,
				localPath: filepath.Join(dir, "config.json"),
			})
		}
		if mt.VocabURL != "" {
			vocabName := mt.VocabName
			if vocabName == "" {
				vocabName = "shared_vocabulary.json"
			}
			extras = append(extras, extraFile{
				url:       mt.VocabURL,
				localPath: filepath.Join(dir, vocabName),
			})
		}
		if mt.TokenizerURL != "" {
			// OPUS-MT publishes source.spm / target.spm rather than the
			// single sentencepiece.model used by MADLAD / m2m100. We
			// keep both filenames so loaders can pick the right one by
			// inspecting which exists.
			spmName := "sentencepiece.model"
			if mt.Backend == "opusmt" {
				spmName = "source.spm"
			}
			extras = append(extras, extraFile{
				url:       mt.TokenizerURL,
				localPath: filepath.Join(dir, spmName),
			})
		}
		if mt.Tokenizer2URL != "" {
			extras = append(extras, extraFile{
				url:       mt.Tokenizer2URL,
				localPath: filepath.Join(dir, "target.spm"),
			})
		}
		row := modelRow{
			id:        name,
			kind:      "mt",
			backend:   mt.Backend,
			label:     "MT " + name,
			sizeMB:    mt.SizeMB,
			tier:      mt.Tier,
			url:       mt.URL,
			sha256:    mt.SHA256,
			manual:    mt.Manual,
			archive:   mt.Archive,
			// model.bin is the canonical "installed" marker; extras
			// (config.json + shared_vocabulary.json + sentencepiece.model)
			// are checked alongside so an incomplete download lights the
			// "missing" badge instead of silently failing at runtime.
			localPath: filepath.Join(dir, "model.bin"),
			extras:    extras,
		}
		if mt.Archive {
			// Archive entries fetch a single .tar.gz which is unpacked
			// into the MT model directory. There are no per-file extras
			// because the tarball already contains everything.
			row.archiveDir = dir
			row.extras = nil
		}
		rows = append(rows, row)
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
// embedded manifest and lets the user download missing ones. Optional
// callbacks let the parent UI react to install events (e.g. hot-reload
// the MT engine).
func ModelsScreen(w fyne.Window, cb ModelsCallbacks) fyne.CanvasObject {
	mf, err := manifest.Load()
	if err != nil {
		return widget.NewLabel("manifest load failed: " + err.Error())
	}
	rows := gatherRows(mf)

	dlClient := downloader.New()
	header := widget.NewLabelWithStyle("Models", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	listBox := container.NewVBox()
	// Engine row goes first: TTS playback depends on the piper binary
	// being installed at all, so the user should see "install me" at
	// the very top before any voice rows.
	listBox.Add(piperEngineRow(w, dlClient, cb.OnPiperInstalled))
	var mu sync.Mutex
	progressByRow := make(map[string]*widget.ProgressBar)
	statusByRow := make(map[string]*widget.Label)

	for _, r := range rows {
		row := r // capture
		statusLbl := widget.NewLabel(rowStatus(row))
		// Long error messages (e.g. a 404 URL pasted into the status)
		// otherwise stretch the row to many screen widths. Truncate
		// at the label widget level so the row width stays sane and
		// the full message remains in the slog output for diagnostics.
		statusLbl.Truncation = fyne.TextTruncateEllipsis
		progress := widget.NewProgressBar()
		progress.Hide()
		var btn *widget.Button
		btn = widget.NewButton(rowAction(row), func() {
			if row.manual {
				// No reliable mirror — point at the README and let the
				// user convert + copy manually. Doing nothing here is
				// the right action; the row label already explains.
				return
			}
			reinstall := row.installed()
			btn.Disable()
			progress.Show()
			go func() {
				if reinstall {
					// Re-download: wipe existing files so the row stops
					// reporting "installed" and the standard fetch path
					// can overwrite cleanly. Stale assets matter when CI
					// has republished a Release tag with new contents.
					removeInstalledFiles(row)
					fyne.Do(func() { statusLbl.SetText("re-downloading…") })
				}
				err := downloadRow(context.Background(), dlClient, row, progress, statusLbl)
				fyne.Do(func() {
					if err != nil {
						// Cap the rendered error so a 200-char message
						// (long HF URL etc.) doesn't blow out row width;
						// the full text remains in slog for diagnosis.
						statusLbl.SetText("error: " + truncateText(err.Error(), 80))
					} else {
						statusLbl.SetText(rowStatus(row))
						btn.SetText(rowAction(row))
					}
					progress.Hide()
					btn.Enable()
				})
				if err == nil && row.kind == "mt" && cb.OnMTInstalled != nil {
					cb.OnMTInstalled(row.backend)
				}
			}()
		})
		if row.manual && !row.installed() {
			btn.Disable()
		}

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
	badge := tierBadge(r.tier)
	if !r.installed() {
		if r.manual {
			return fmt.Sprintf("%smanual install — see README (~%d MB)", badge, r.sizeMB)
		}
		return fmt.Sprintf("%smissing (%d MB)", badge, r.sizeMB)
	}
	size := paths.FileSize(r.localPath)
	for _, e := range r.extras {
		size += paths.FileSize(e.localPath)
	}
	return fmt.Sprintf("%sinstalled (%d MB)", badge, size>>20)
}

// tierBadge renders a short tier prefix shown next to every model row.
// Realtime entries are marked first so the eye lands on them, since
// they are the ones the application actually recommends for live use.
func tierBadge(t manifest.Tier) string {
	switch t {
	case manifest.TierRealtime:
		return "[REALTIME ★]  "
	case manifest.TierBalanced:
		return "[balanced]    "
	case manifest.TierQuality:
		return "[quality]     "
	default:
		return ""
	}
}

func rowAction(r modelRow) string {
	if r.installed() {
		return "Re-download"
	}
	if r.manual {
		return "Manual install"
	}
	return "Download"
}

// truncateText shortens s to at most max runes, appending an ellipsis
// when truncation actually happened. Used to keep error rows from
// blowing out the Models tab layout.
func truncateText(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	return string(rs[:max-1]) + "…"
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

// removeInstalledFiles deletes a row's primary file and any sidecar
// extras, best-effort. Used by the Re-download path so the next fetch
// starts from a clean slate (matters when a CI-republished Release tag
// has the same URL but different contents).
func removeInstalledFiles(r modelRow) {
	_ = os.Remove(r.localPath)
	for _, e := range r.extras {
		if e.localPath != "" {
			_ = os.Remove(e.localPath)
		}
	}
}

func downloadRow(ctx context.Context, d *downloader.Downloader, r modelRow, bar *widget.ProgressBar, status *widget.Label) error {
	if r.url == "" {
		return fmt.Errorf("no URL")
	}

	if r.archive {
		// Archive flow: fetch a single .tar.gz into a tmp path,
		// unpack into archiveDir, drop the tarball afterwards. Saves
		// the user from the Python+torch+ctranslate2 toolchain entirely.
		tmp := filepath.Join(r.archiveDir, "_download.tar.gz")
		ch, err := d.Fetch(ctx, r.url, tmp, r.sha256)
		if err != nil {
			return err
		}
		for p := range ch {
			updateProgress(bar, status, p)
		}
		// downloader.Fetch's run goroutine logs HTTP failures to
		// stderr but does not surface them to the caller. Detect the
		// failure here by checking whether the file actually landed —
		// the misleading "unpack: open archive: no such file" message
		// is what users were seeing before this guard.
		if !paths.Exists(tmp) || paths.FileSize(tmp) == 0 {
			return fmt.Errorf("download failed — no archive at %s (check stderr / GitHub Release for the actual HTTP status; the model may be a 404)", tmp)
		}
		fyne.Do(func() { status.SetText("unpacking…") })
		if err := downloader.ExtractTarGz(tmp, r.archiveDir); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("unpack: %w", err)
		}
		_ = os.Remove(tmp)
		if !paths.Exists(r.localPath) || paths.FileSize(r.localPath) == 0 {
			return fmt.Errorf("unpack finished but %s is missing", r.localPath)
		}
		return nil
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
