package service

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

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
