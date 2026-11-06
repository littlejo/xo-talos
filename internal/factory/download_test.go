package factory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const testSchematic = "53b20d86399013eadfd44ee49804c1fef069bfdee3b43f3f3f5a2f57c03338ac"

func compress(t *testing.T, ext string, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	var err error
	switch ext {
	case ".raw.zst":
		w, err = zstd.NewWriter(&buf)
	case ".raw.xz":
		w, err = xz.NewWriter(&buf)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serve returns a factory that only has the image with the given suffix.
func serve(t *testing.T, ext string, raw []byte, hits *atomic.Int32) *Client {
	t.Helper()
	body := compress(t, ext, raw)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/nocloud-amd64"+ext) {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestDownloadAndOpenRaw(t *testing.T) {
	raw := bytes.Repeat([]byte("talos"), 100000)
	for _, ext := range compressions {
		t.Run(ext, func(t *testing.T) {
			var hits atomic.Int32
			c := serve(t, ext, raw, &hits)
			dir := t.TempDir()

			path, cached, err := c.Download(context.Background(), testSchematic, "v1.14.5", "nocloud", "amd64", dir)
			if err != nil {
				t.Fatal(err)
			}
			if cached || !strings.HasSuffix(path, ext) {
				t.Fatalf("Download = %s, cached %v", path, cached)
			}
			r, err := OpenRaw(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("decompressed image differs (err %v)", err)
			}

			// A second download reuses the file.
			if _, cached, err := c.Download(context.Background(), testSchematic, "v1.14.5", "nocloud", "amd64", dir); err != nil || !cached {
				t.Fatalf("second Download: cached %v, err %v", cached, err)
			}
			if hits.Load() != 1 {
				t.Fatalf("image downloaded %d times", hits.Load())
			}
			if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(leftovers) > 0 {
				t.Fatalf("temporary files left: %v", leftovers)
			}
		})
	}
}

func TestDownloadMissingImage(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dir := t.TempDir()
	if _, _, err := New(srv.URL).Download(context.Background(), testSchematic, "v1.14.5", "nocloud", "amd64", dir); err == nil {
		t.Fatal("Download succeeded without an image")
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		t.Fatalf("files left in the work dir: %v", entries)
	}
}

func TestDownloadInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("partial"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, _, err := New(srv.URL).Download(context.Background(), testSchematic, "v1.14.5", "nocloud", "amd64", dir); err == nil {
		t.Fatal("Download accepted a truncated body")
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		t.Fatalf("a truncated image was left for the next run: %v", entries)
	}
}
