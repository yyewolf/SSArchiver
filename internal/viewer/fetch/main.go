// Command fetch downloads the ArcViewer files listed in manifest.txt,
// verifies their sha256 and writes gzip'd copies to ./dist (run by
// `go generate ./internal/viewer`, cwd = internal/viewer).
package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/viewer"
)

func main() {
	if err := run(context.Background(), "dist"); err != nil {
		fmt.Fprintln(os.Stderr, "viewer fetch:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out string) error {
	entries, err := viewer.ParseManifest(viewer.Manifest)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(viewer.Manifest))
	stamp := hex.EncodeToString(sum[:])
	if b, err := os.ReadFile(filepath.Join(out, ".stamp")); err == nil && strings.TrimSpace(string(b)) == stamp {
		fmt.Println("viewer bundle up to date")
		return nil
	}
	tmp := out + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	hc := &http.Client{Timeout: 10 * time.Minute}
	for _, e := range entries {
		data, err := download(ctx, hc, viewer.RawBaseURL+e.Commit+"/"+e.Path)
		if err != nil {
			return err
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) != e.SHA256 {
			return fmt.Errorf("checksum mismatch for %s: got %x", e.Path, got)
		}
		if err := writeGzip(filepath.Join(tmp, filepath.FromSlash(e.Path))+".gz", data); err != nil {
			return err
		}
		fmt.Printf("  %-60s %8d KiB\n", e.Path, len(data)/1024)
	}
	if err := os.WriteFile(filepath.Join(tmp, "PLACEHOLDER"), []byte(viewer.PlaceholderText), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".stamp"), []byte(stamp+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

func download(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

func writeGzip(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err := zw.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
