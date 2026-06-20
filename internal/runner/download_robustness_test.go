package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// httpDownload must create the destination's parent directory if it is absent -
// install assumes nothing pre-exists, so e.g. {installRoot}/.cache works on a
// fresh host or after a full uninstall removed installRoot (regression: the
// ".cache/...tar.gz.tmp: no such file or directory" bug found live on ARMORA).
func TestHTTPDownloadCreatesMissingParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	// A nested, not-yet-existing path mirroring {installRoot}/.cache/<artifact>.
	dst := filepath.Join(t.TempDir(), "actions-runners", ".cache", "actions-runner-linux-x64.tar.gz")
	if err := httpDownload(context.Background(), srv.URL, dst); err != nil {
		t.Fatalf("httpDownload into a missing parent dir: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(b) != "payload" {
		t.Fatalf("content = %q, want %q", b, "payload")
	}
}
