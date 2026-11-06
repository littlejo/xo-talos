// Package factory talks to the Talos Image Factory: it registers schematics,
// resolves Talos versions and downloads disk images.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/littlejo/xo-talos/internal/progress"
)

// DefaultURL is the public Image Factory.
const DefaultURL = "https://factory.talos.dev"

// Client is an Image Factory client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Progress reports the download progress; nil reports nothing.
	Progress *progress.Reporter
}

// New returns a client for the factory at baseURL.
func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

// CreateSchematic registers a schematic and returns its ID. The ID is a hash
// of the schematic's content, so the same schematic always gives the same ID.
// The factory validates the schematic and rejects unknown fields.
func (c *Client) CreateSchematic(ctx context.Context, s Schematic) (string, error) {
	body, err := s.YAML()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/schematics", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/yaml")

	var resp struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(req, &resp); err != nil {
		return "", fmt.Errorf("create schematic: %w", err)
	}
	if resp.ID == "" {
		return "", errors.New("create schematic: empty id in response")
	}
	return resp.ID, nil
}

// ResolveVersion turns a version request into a Talos version the factory
// serves:
//   - "" or "latest": the newest stable release;
//   - "v1.13" (or "1.13"): the newest stable patch release of that minor;
//   - anything else, e.g. "v1.14.5" or "v1.15.0-alpha.2": that exact version,
//     which must exist.
func (c *Client) ResolveVersion(ctx context.Context, want string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/versions", nil)
	if err != nil {
		return "", err
	}
	var versions []string
	if err := c.doJSON(req, &versions); err != nil {
		return "", fmt.Errorf("list versions: %w", err)
	}
	return ResolveVersion(versions, want)
}

// ResolveVersion is Client.ResolveVersion on a known list of versions.
func ResolveVersion(versions []string, want string) (string, error) {
	switch {
	case want == "" || want == "latest":
		if v, ok := latestStable(versions, nil); ok {
			return v, nil
		}
		return "", errors.New("no stable Talos version found")
	case isMinor(want):
		minor := strings.Split(strings.TrimPrefix(want, "v"), ".")
		x, _ := strconv.Atoi(minor[0])
		y, _ := strconv.Atoi(minor[1])
		if v, ok := latestStable(versions, []int{x, y}); ok {
			return v, nil
		}
		return "", fmt.Errorf("no stable Talos %s.x release found", want)
	default:
		if !strings.HasPrefix(want, "v") {
			want = "v" + want
		}
		if slices.Contains(versions, want) {
			return want, nil
		}
		return "", fmt.Errorf("version %s is not available from the Talos Image Factory", want)
	}
}

// isMinor reports whether v names a minor release line such as "v1.13".
func isMinor(v string) bool {
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(fields) != 2 {
		return false
	}
	for _, f := range fields {
		if _, err := strconv.Atoi(f); err != nil {
			return false
		}
	}
	return true
}

// latestStable returns the highest vX.Y.Z version in versions whose first
// numbers equal prefix, ignoring pre-releases and anything it cannot parse.
func latestStable(versions []string, prefix []int) (string, bool) {
	var best string
	var bestParts []int
	for _, v := range versions {
		parts, ok := parseStable(v)
		if !ok || !slices.Equal(parts[:len(prefix)], prefix) {
			continue
		}
		if bestParts == nil || slices.Compare(parts, bestParts) > 0 {
			best, bestParts = v, parts
		}
	}
	return best, bestParts != nil
}

func parseStable(v string) ([]int, bool) {
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(fields) != 3 {
		return nil, false
	}
	parts := make([]int, 3)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, false
		}
		parts[i] = n
	}
	return parts, true
}

// CheckExtensions returns an error naming every extension that is not an
// official extension for the given Talos version.
func (c *Client) CheckExtensions(ctx context.Context, version string, extensions []string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/version/"+version+"/extensions/official", nil)
	if err != nil {
		return err
	}
	var official []struct {
		Name string `json:"name"`
	}
	if err := c.doJSON(req, &official); err != nil {
		return fmt.Errorf("list official extensions for %s: %w", version, err)
	}

	available := make(map[string]bool, len(official))
	for _, o := range official {
		available[o.Name] = true
	}
	var missing []string
	for _, ext := range extensions {
		if !available[ext] {
			missing = append(missing, ext)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("extensions not available for Talos %s: %s", version, strings.Join(missing, ", "))
	}
	return nil
}

// compressions are the image suffixes the factory serves, in order of
// preference: newer Talos versions are served as .raw.zst, older ones as
// .raw.xz.
var compressions = []string{".raw.zst", ".raw.xz"}

// Download downloads the compressed disk image <platform>-<arch> of a
// schematic and version into dir and returns its path. It never writes the
// decompressed disk: OpenRaw decompresses it on the fly. An image already
// downloaded to dir is reused, since factory images never change; cached
// reports it.
func (c *Client) Download(ctx context.Context, schematic, version, platform, arch, dir string) (path string, cached bool, err error) {
	name := platform + "-" + arch
	base := filepath.Join(dir, fmt.Sprintf("talos-%s-%s-%s", version, name, schematic[:12]))
	for _, ext := range compressions {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext, true, nil
		}
	}

	for _, ext := range compressions {
		url := fmt.Sprintf("%s/image/%s/%s/%s%s", c.BaseURL, schematic, version, name, ext)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", false, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return "", false, fmt.Errorf("download %s: %w", url, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return "", false, fmt.Errorf("download %s: %s", url, resp.Status)
		}
		pr := c.Progress.Wrap(resp.Body, resp.ContentLength, "download")
		err = saveFile(base+ext, pr)
		_ = resp.Body.Close()
		if err != nil {
			return "", false, fmt.Errorf("download %s: %w", url, err)
		}
		pr.Finish()
		return base + ext, false, nil
	}
	return "", false, fmt.Errorf("no %s image for schematic %s and Talos %s", name, schematic, version)
}

// saveFile writes r to path through a temporary file, so an interrupted
// download never leaves a truncated image that a later run would reuse.
func saveFile(path string, r io.Reader) error {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// OpenRaw opens a compressed image saved by Download and returns the
// decompressed raw disk.
func OpenRaw(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(path, ".raw.zst"):
		d, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return &decoder{Reader: d, close: func() error { d.Close(); return f.Close() }}, nil
	case strings.HasSuffix(path, ".raw.xz"):
		d, err := xz.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return &decoder{Reader: d, close: f.Close}, nil
	default:
		_ = f.Close()
		return nil, fmt.Errorf("unknown image compression: %s", path)
	}
}

type decoder struct {
	io.Reader
	close func() error
}

func (d *decoder) Close() error { return d.close() }

func (c *Client) doJSON(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return json.Unmarshal(body, out)
}
