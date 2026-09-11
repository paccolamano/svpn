// Package release finds, downloads and verifies a published build.
//
// It exists because GitHub Releases is the only distribution channel: there is
// no package manager to notice that a fix has shipped, so the binary has to be
// able to ask. What it can promise is limited and worth stating plainly — the
// archive and its checksums both come from the same GitHub release, so the
// SHA-256 check catches a truncated or corrupted download, not a compromised
// publisher. Provenance is what the build attestations are for, and verifying
// those needs the gh CLI.
package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sorintlab/errors"
)

// DefaultRepository is where svpn is published.
const DefaultRepository = "paccolamano/svpn"

// ChecksumsAsset is the file listing the SHA-256 of every archive, as named by
// the goreleaser configuration.
const ChecksumsAsset = "SHA256SUMS"

// maxAssetSize bounds a download. The archives are tens of megabytes; this is
// far above that and still refuses to fill a disk with whatever an unexpected
// redirect leads to.
const maxAssetSize = 256 << 20

// requestTimeout bounds resolving a release. Downloading gets its own, longer,
// budget from the caller's context.
const requestTimeout = 30 * time.Second

// Release is a published version and the files it carries.
type Release struct {
	// Tag is the git tag, e.g. "v1.2.3".
	Tag    string
	Assets []Asset
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Client talks to the release host.
type Client struct {
	// Repository is "owner/name". Empty selects DefaultRepository.
	Repository string
	// HTTP is the client used for every request. Nil selects a default with a
	// timeout; tests point it at their own server.
	HTTP *http.Client
	// BaseURL is where the API lives. Empty selects GitHub's; tests override it.
	BaseURL string
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}

	return &http.Client{Timeout: requestTimeout}
}

func (c *Client) repository() string {
	if c.Repository != "" {
		return c.Repository
	}

	return DefaultRepository
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}

	return "https://api.github.com"
}

// Latest reports the most recent published release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := c.baseURL() + "/repos/" + c.repository() + "/releases/latest"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, errors.Wrapf(err, "building the request for %s", url)
	}
	request.Header.Set("Accept", "application/vnd.github+json")

	response, err := c.httpClient().Do(request)
	if err != nil {
		return Release{}, errors.Wrapf(err, "asking %s for the latest release", c.repository())
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotFound {
		// Nothing has been tagged yet, which is a different situation from a
		// failure and reads as one without saying so.
		return Release{}, errors.Errorf("%s has published no releases yet", c.repository())
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, errors.Errorf("%s answered %s", url, response.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return Release{}, errors.Wrapf(err, "reading the release description")
	}

	found := Release{Tag: payload.TagName}
	for _, asset := range payload.Assets {
		found.Assets = append(found.Assets, Asset{Name: asset.Name, URL: asset.URL, Size: asset.Size})
	}

	return found, nil
}

// Archive picks the archive built for one platform.
func (r Release) Archive(goos, goarch string) (Asset, error) {
	suffix := "_" + goos + "_" + goarch + ".tar.gz"

	for _, asset := range r.Assets {
		if strings.HasSuffix(asset.Name, suffix) {
			return asset, nil
		}
	}

	// The daemon only works on Linux, so this is the expected answer on macOS
	// and Windows rather than a sign that something went wrong.
	return Asset{}, errors.Errorf("release %s carries no build for %s/%s", r.Tag, goos, goarch)
}

// Checksums finds the file listing the archives' SHA-256 sums.
func (r Release) Checksums() (Asset, error) {
	for _, asset := range r.Assets {
		if asset.Name == ChecksumsAsset {
			return asset, nil
		}
	}

	return Asset{}, errors.Errorf("release %s publishes no %s", r.Tag, ChecksumsAsset)
}

// Download fetches asset into dir and returns the path it was written to,
// after checking it against the release's checksums.
//
// The checksum is not optional: an archive is about to be unpacked over the
// binaries that run as root, and a download that silently truncated is a real
// failure mode on a laptop that changed networks halfway through.
func (c *Client) Download(ctx context.Context, asset, checksums Asset, dir string) (string, error) {
	sums, err := c.fetchChecksums(ctx, checksums)
	if err != nil {
		return "", err
	}

	want, ok := sums[asset.Name]
	if !ok {
		return "", errors.Errorf("%s does not list %s", ChecksumsAsset, asset.Name)
	}

	// The path is built from a temporary directory this process made and an
	// asset name from the release listing; nothing here comes from a caller.
	path := filepath.Join(dir, asset.Name)
	file, err := os.Create(path) //nolint:gosec
	if err != nil {
		return "", errors.Wrapf(err, "creating %s", path)
	}

	digest := sha256.New()
	if err := c.fetch(ctx, asset.URL, io.MultiWriter(file, digest)); err != nil {
		_ = file.Close()

		return "", err
	}
	if err := file.Close(); err != nil {
		return "", errors.Wrapf(err, "closing %s", path)
	}

	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		return "", errors.Errorf("%s does not match its published checksum (got %s, want %s); the download was corrupted or the release was changed",
			asset.Name, got, want)
	}

	return path, nil
}

// fetchChecksums reads the published sums, keyed by file name.
func (c *Client) fetchChecksums(ctx context.Context, checksums Asset) (map[string]string, error) {
	var body strings.Builder
	if err := c.fetch(ctx, checksums.URL, &body); err != nil {
		return nil, err
	}

	return parseChecksums(body.String()), nil
}

// parseChecksums reads the "<hex>  <name>" lines sha256sum produces.
func parseChecksums(body string) map[string]string {
	sums := map[string]string{}

	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		// The name may carry the "*" that marks a binary-mode checksum.
		sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}

	return sums
}

// fetch copies one URL into destination.
func (c *Client) fetch(ctx context.Context, url string, destination io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.Wrapf(err, "building the request for %s", url)
	}

	response, err := c.httpClient().Do(request)
	if err != nil {
		return errors.Wrapf(err, "downloading %s", url)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return errors.Errorf("%s answered %s", url, response.Status)
	}

	if _, err := io.Copy(destination, io.LimitReader(response.Body, maxAssetSize)); err != nil {
		return errors.Wrapf(err, "reading %s", url)
	}

	return nil
}

// Extract unpacks a release archive into dir and returns the directory holding
// its files.
func Extract(archive, dir string) (string, error) {
	// archive is what Download just wrote and verified against the published
	// checksum, so opening it by name is not opening something a caller chose.
	file, err := os.Open(archive) //nolint:gosec
	if err != nil {
		return "", errors.Wrapf(err, "opening %s", archive)
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		return "", errors.Wrapf(err, "reading %s as gzip", archive)
	}
	defer func() { _ = compressed.Close() }()

	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.Wrapf(err, "reading %s", archive)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		// An entry naming ".." or an absolute path would write outside dir.
		// The archive comes from our own release, but an extractor that trusts
		// its input is a bug waiting for the day one does not. Rooting the name
		// at "/" and cleaning it is what defuses that, and the prefix check
		// below is what proves it; gosec cannot see through either.
		name := filepath.Clean(filepath.Join("/", header.Name)) //nolint:gosec
		path := filepath.Join(dir, name)
		if !strings.HasPrefix(path, filepath.Clean(dir)+string(os.PathSeparator)) {
			return "", errors.Errorf("%s contains an entry outside the archive: %s", archive, header.Name)
		}

		if err := writeEntry(path, reader, header.FileInfo().Mode()); err != nil {
			return "", err
		}
	}

	return dir, nil
}

// writeEntry writes one file out of an archive.
// The path was checked against the destination directory by the caller, which
// is what makes writing an archive entry by name safe here.
func writeEntry(path string, source io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec
		return errors.Wrapf(err, "creating %s", filepath.Dir(path))
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()) //nolint:gosec
	if err != nil {
		return errors.Wrapf(err, "creating %s", path)
	}

	if _, err := io.Copy(file, io.LimitReader(source, maxAssetSize)); err != nil {
		_ = file.Close()

		return errors.Wrapf(err, "writing %s", path)
	}

	return errors.Wrapf(file.Close(), "closing %s", path)
}

// semver matches the tags a release carries. A build made by the Makefile from
// an untagged commit is a bare hash, possibly with -dirty, and does not match.
var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

// IsRelease reports whether version names a published release rather than a
// development build.
//
// It is what stops "svpnd update" from replacing a binary someone is working on
// with the last published one, which would silently discard their build.
func IsRelease(version string) bool {
	return semver.MatchString(version)
}

// Newer reports whether candidate is a later release than current.
//
// A current version that is not a release compares as older than everything,
// so a development build is always offered the update — it is refused higher
// up, where there is a --force to override it.
func Newer(candidate, current string) bool {
	right, ok := parseVersion(current)
	if !ok {
		return true
	}

	left, ok := parseVersion(candidate)
	if !ok {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}

	return false
}

// parseVersion pulls the major, minor and patch numbers out of a tag.
func parseVersion(version string) ([3]int, bool) {
	match := semver.FindStringSubmatch(version)
	if match == nil {
		return [3]int{}, false
	}

	var parsed [3]int
	for i := range parsed {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		parsed[i] = value
	}

	return parsed, true
}
