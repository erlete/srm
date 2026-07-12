package service

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupConfig writes a timestamped backup of the config dir next to the config
// file and returns the path. The safety net before any destructive lifecycle op.
func (m *Manager) BackupConfig() (string, error) {
	if m.cfgPath == "" {
		return "", fmt.Errorf("no config path is set - nothing to back up")
	}
	dir := filepath.Dir(m.cfgPath)
	out := filepath.Join(dir, fmt.Sprintf("srm-backup-%d.tar.gz", time.Now().Unix()))
	return BackupConfigDir(dir, out)
}

// BackupInfo describes one restorable config-dir archive.
type BackupInfo struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

// ListBackups finds srm config-dir backup archives ("srm-backup-*.tar.gz" and
// "srm-config-*.tar.gz" - the names BackupConfig and the pre-purge backup write) in
// dir, newest first. A missing dir yields an empty list, not an error, so the restore
// picker degrades to "no backups found" rather than failing.
func ListBackups(dir string) ([]BackupInfo, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BackupInfo
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".tar.gz") || !(strings.HasPrefix(name, "srm-backup-") || strings.HasPrefix(name, "srm-config-")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Path: filepath.Join(dir, name), Name: name, Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// BackupConfigDir writes a gzip-compressed tar of every regular file directly
// inside srcDir (the srm config dir: config.yaml, secrets.age, per-org *.pem) to
// outPath (0600), returning the path written. It is the safety net taken before
// destructive lifecycle ops - the GitHub App private key is shown once by GitHub
// and is otherwise unrecoverable. The config dir is flat, so subdirectories are
// not recursed; entries are stored under their bare basename so restore can never
// be tricked into a path-traversal write.
func BackupConfigDir(srcDir, outPath string) (string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("read config dir %s: %w", srcDir, err)
	}

	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		hdr := &tar.Header{
			Name:    e.Name(), // bare basename - traversal-safe on restore
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return "", err
		}
		src, err := os.Open(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(tw, src); err != nil {
			src.Close()
			return "", err
		}
		src.Close()
		n++
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("no files found in config dir %s", srcDir)
	}
	return outPath, nil
}

// RestoreConfigDir extracts a BackupConfigDir archive into destDir (created 0700
// if absent), restoring each file's archived permission bits. Entry names must be
// plain basenames; anything containing a path separator or ".." is rejected to
// prevent path traversal (zip-slip).
func RestoreConfigDir(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := hdr.Name
		if name == "" || name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("refusing unsafe archive entry %q", name)
		}
		dst := filepath.Join(destDir, name)
		mode := os.FileMode(hdr.Mode) & 0o777
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		// Re-assert mode in case umask masked the O_CREATE bits (secrets are 0600).
		if err := os.Chmod(dst, mode); err != nil {
			return err
		}
	}
	return nil
}
