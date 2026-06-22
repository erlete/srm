package runner

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractZip unpacks a .zip archive (the Windows actions/runner asset shape) into
// dst. It mirrors extractTarGz's safety: every entry is confined to dst (a Zip-Slip
// path-traversal guard), directories are created as needed, and regular files are
// written with their recorded mode. Windows ignores the POSIX exec bit, so the
// runner's config.cmd/run.cmd/bin executables are launchable as-is once extracted.
func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	clean := filepath.Clean(dst)
	for _, f := range zr.File {
		target := filepath.Join(dst, f.Name)
		// Zip-Slip guard: the resolved path must stay inside dst.
		if target != clean && !strings.HasPrefix(target, clean+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe zip path: %s", f.Name)
		}
		info := f.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		// Some zips omit explicit directory entries; ensure the parent exists.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipFile(f, target, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

// writeZipFile streams one zip entry to target. Split out so the rc.Close() defer
// is scoped per file (not leaked across the whole archive loop).
func writeZipFile(f *zip.File, target string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
