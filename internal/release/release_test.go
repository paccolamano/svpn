package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRelease(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "v1.2.3", want: true},
		{version: "v0.0.1", want: true},
		{version: "v1.2.3-rc1", want: true},
		// What scripts/git-version.sh produces from an untagged commit, which
		// is what must not be silently replaced by an update.
		{version: "c30f4933772a4c1e0d9a", want: false},
		{version: "v1.2.3-dirty", want: true},
		{version: "unknown", want: false},
		{version: "", want: false},
	}

	for _, test := range tests {
		if got := IsRelease(test.version); got != test.want {
			t.Errorf("IsRelease(%q) = %v, want %v", test.version, got, test.want)
		}
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "v1.2.4", current: "v1.2.3", want: true},
		{candidate: "v1.3.0", current: "v1.2.9", want: true},
		{candidate: "v2.0.0", current: "v1.9.9", want: true},
		{candidate: "v1.2.3", current: "v1.2.3", want: false},
		{candidate: "v1.2.2", current: "v1.2.3", want: false},
		// Double digits must not compare as strings, or v1.10.0 would look
		// older than v1.9.0.
		{candidate: "v1.10.0", current: "v1.9.0", want: true},
		// A development build compares as older than anything, so the refusal
		// to replace it is a deliberate decision higher up rather than an
		// accident of comparison.
		{candidate: "v1.0.0", current: "c30f4933", want: true},
		{candidate: "not-a-version", current: "v1.0.0", want: false},
	}

	for _, test := range tests {
		if got := Newer(test.candidate, test.current); got != test.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", test.candidate, test.current, got, test.want)
		}
	}
}

func TestArchivePicksThePlatformBuild(t *testing.T) {
	found := Release{
		Tag: "v1.0.0",
		Assets: []Asset{
			{Name: "svpn_1.0.0_linux_arm64.tar.gz", URL: "http://example/arm"},
			{Name: "svpn_1.0.0_linux_amd64.tar.gz", URL: "http://example/amd"},
			{Name: ChecksumsAsset, URL: "http://example/sums"},
		},
	}

	archive, err := found.Archive("linux", "amd64")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archive.URL != "http://example/amd" {
		t.Errorf("archive = %+v, want the amd64 one", archive)
	}

	// The daemon is Linux-only, so this is the expected answer elsewhere and
	// the message has to say which platform was asked for.
	if _, err := found.Archive("darwin", "arm64"); err == nil {
		t.Error("Archive succeeded for a platform with no build")
	} else if !strings.Contains(err.Error(), "darwin/arm64") {
		t.Errorf("error = %v, want it to name the platform", err)
	}
}

func TestChecksumsAssetIsRequired(t *testing.T) {
	found := Release{Tag: "v1.0.0", Assets: []Asset{{Name: "svpn_1.0.0_linux_amd64.tar.gz"}}}

	if _, err := found.Checksums(); err == nil {
		t.Error("Checksums succeeded on a release that publishes none")
	}
}

func TestParseChecksums(t *testing.T) {
	sums := parseChecksums("abc123  svpn_1.0.0_linux_amd64.tar.gz\ndef456 *svpn_1.0.0_linux_arm64.tar.gz\n\n")

	if sums["svpn_1.0.0_linux_amd64.tar.gz"] != "abc123" {
		t.Errorf("sums = %v", sums)
	}
	// sha256sum marks a binary-mode entry with a leading star.
	if sums["svpn_1.0.0_linux_arm64.tar.gz"] != "def456" {
		t.Errorf("sums = %v, want the binary-mode entry parsed", sums)
	}
}

// releaseServer serves one archive, its checksums and the release listing.
func releaseServer(t *testing.T, archive []byte, sum string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/repos/owner/name/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{
			"tag_name": "v1.2.3",
			"assets": [
				{"name": "svpn_1.2.3_linux_amd64.tar.gz", "browser_download_url": %q, "size": %d},
				{"name": %q, "browser_download_url": %q, "size": 100}
			]
		}`, server.URL+"/archive", len(archive), ChecksumsAsset, server.URL+"/sums")
	})
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  svpn_1.2.3_linux_amd64.tar.gz\n", sum)
	})

	return server
}

// tarball builds a gzipped tar holding the two binaries a release carries.
func tarball(t *testing.T) []byte {
	t.Helper()

	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)

	for _, name := range []string{"svpn", "svpnd"} {
		body := "binary " + name
		if err := archive.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("writing the header for %s: %v", name, err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatalf("closing the archive: %v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("closing the gzip stream: %v", err)
	}

	return buffer.Bytes()
}

func TestDownloadVerifiesTheChecksum(t *testing.T) {
	archive := tarball(t)
	digest := sha256.Sum256(archive)
	server := releaseServer(t, archive, hex.EncodeToString(digest[:]))

	client := &Client{Repository: "owner/name", BaseURL: server.URL}

	latest, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Tag != "v1.2.3" {
		t.Errorf("tag = %q", latest.Tag)
	}

	asset, err := latest.Archive("linux", "amd64")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	checksums, err := latest.Checksums()
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	dir := t.TempDir()
	path, err := client.Download(context.Background(), asset, checksums, dir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if _, err := Extract(path, dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, name := range []string{"svpn", "svpnd"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not extracted: %v", name, err)
		}
		// The extracted svpnd is about to be executed to install itself, so
		// losing the mode would turn the update into a permission error.
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable: %v", name, info.Mode())
		}
	}
}

func TestDownloadRefusesAMismatchedChecksum(t *testing.T) {
	// A download that truncated halfway is the realistic case here, and it is
	// about to be unpacked over binaries that run as root.
	server := releaseServer(t, tarball(t), strings.Repeat("0", 64))

	client := &Client{Repository: "owner/name", BaseURL: server.URL}

	latest, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	asset, _ := latest.Archive("linux", "amd64")
	checksums, _ := latest.Checksums()

	if _, err := client.Download(context.Background(), asset, checksums, t.TempDir()); err == nil {
		t.Fatal("Download accepted an archive that does not match its checksum")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %v, want it to name the checksum", err)
	}
}

func TestExtractRefusesAnEntryOutsideTheDestination(t *testing.T) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)

	// Zip Slip: an entry that escapes the destination directory. The archive
	// is our own, but an extractor that trusts its input is a bug in waiting.
	body := "owned"
	if err := archive.WriteHeader(&tar.Header{
		Name: "../../etc/cron.d/evil", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("writing the header: %v", err)
	}
	if _, err := archive.Write([]byte(body)); err != nil {
		t.Fatalf("writing the body: %v", err)
	}
	_ = archive.Close()
	_ = compressed.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "evil.tar.gz")
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("writing the archive: %v", err)
	}

	destination := t.TempDir()
	if _, err := Extract(path, destination); err != nil {
		// Rooting the name at "/" turns the traversal into a plain path, so
		// the entry lands inside the destination rather than being refused.
		// Either outcome is safe; escaping is not.
		t.Logf("Extract refused the archive: %v", err)
	}

	escaped := filepath.Join(filepath.Dir(filepath.Dir(destination)), "etc", "cron.d", "evil")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("the entry escaped to %s", escaped)
	}
}

func TestLatestSaysWhenNothingIsPublished(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/name/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := &Client{Repository: "owner/name", BaseURL: server.URL}

	_, err := client.Latest(context.Background())
	if err == nil {
		t.Fatal("Latest succeeded against a repository with no releases")
	}
	// Before the first tag this is the normal answer, and "404" would read as
	// something being broken.
	if !strings.Contains(err.Error(), "no releases yet") {
		t.Errorf("error = %v, want it to say nothing is published", err)
	}
}
