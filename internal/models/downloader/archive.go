package downloader

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTarGz unpacks a .tar.gz archive into destDir. Existing files
// are overwritten. Only regular files and directories are restored;
// symlinks and devices are skipped on purpose so a malicious archive
// can't link outside destDir.
//
// The path-traversal check (rejecting absolute paths and "..") is
// strict — every member must resolve inside destDir or the extraction
// aborts with an error.
func ExtractTarGz(srcPath, destDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("downloader: open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("downloader: gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	cleanDest := filepath.Clean(destDir) + string(filepath.Separator)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("downloader: tar: %w", err)
		}
		// Strip any leading top-level directory so an archive of
		// `m2m100-418m-int8/model.bin` lands as `<destDir>/model.bin`,
		// regardless of how the archive was created.
		name := stripTopLevel(hdr.Name)
		if name == "" {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		// Path-traversal guard: target must stay under destDir.
		if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), cleanDest) &&
			filepath.Clean(target) != filepath.Clean(destDir) {
			return fmt.Errorf("downloader: tar entry escapes dest: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// The piper release tarball wires up its bundled dylibs via
			// symlinks (libespeak-ng.1.dylib → libespeak-ng.1.51.dylib);
			// silently skipping them breaks @rpath lookup at runtime.
			// Resolve the link target *relative to the symlink's parent
			// directory* and reject anything that escapes destDir so a
			// malicious archive cannot link out via "../../etc/passwd".
			linkRel := filepath.FromSlash(hdr.Linkname)
			var absLink string
			if filepath.IsAbs(linkRel) {
				absLink = filepath.Clean(linkRel)
			} else {
				absLink = filepath.Clean(filepath.Join(filepath.Dir(target), linkRel))
			}
			if !strings.HasPrefix(absLink+string(filepath.Separator), cleanDest) &&
				absLink != filepath.Clean(destDir) {
				return fmt.Errorf("downloader: symlink %q escapes dest (target=%q)", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Overwrite any existing entry — re-installs depend on this.
			_ = os.Remove(target)
			if err := os.Symlink(linkRel, target); err != nil {
				return fmt.Errorf("downloader: symlink: %w", err)
			}
		default:
			// Skip hardlinks, char/block devices, fifos. Models from our
			// CI workflow only contain regular files; piper tarballs
			// only need regular files + symlinks (handled above).
		}
	}
}

// stripTopLevel removes the first path segment of name when there is
// more than one segment, so archive layouts like "<model>/model.bin"
// flatten to "model.bin" relative to destDir. Bare filenames pass
// through unchanged.
func stripTopLevel(name string) string {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return ""
	}
	_, after, ok := strings.Cut(name, "/")
	if !ok {
		return name
	}
	rest := after
	return rest
}

// IsArchiveURL is a tiny convenience used by the Models UI to decide
// whether the downloaded payload should be unpacked.
func IsArchiveURL(url string) bool {
	return strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz")
}
