package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyInstall(t *testing.T) {
	for _, tt := range []struct {
		version, exe string
		wantContains string // "" = updatable
	}{
		{"v1.3.0", "/home/u/.local/bin/jog", ""},
		{"(devel)", "/home/u/go/bin/jog", "go install"},
		{"v0.0.0-20260809120000-abcdef123456", "/home/u/go/bin/jog", "go install"},
		{"", "/usr/local/bin/jog", "go install"},
		{"v1.3.0", "/opt/homebrew/Cellar/jog/1.3.0/bin/jog", "brew upgrade"},
		{"v1.3.0", "/home/linuxbrew/.linuxbrew/bin/jog", "brew upgrade"},
		{"v1.3.0", `C:\Users\u\AppData\Local\Programs\jog\jog.exe`, ""},
	} {
		got := classifyInstall(tt.version, tt.exe)
		if tt.wantContains == "" && got != "" {
			t.Errorf("classifyInstall(%q, %q) = %q, want updatable", tt.version, tt.exe, got)
		}
		if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
			t.Errorf("classifyInstall(%q, %q) = %q, want mention of %q", tt.version, tt.exe, got, tt.wantContains)
		}
	}
}

func TestIsNewer(t *testing.T) {
	for _, tt := range []struct {
		current, latest string
		want            bool
	}{
		{"v1.3.0", "v1.3.1", true},
		{"v1.3.0", "v1.4.0", true},
		{"v1.3.0", "v2.0.0", true},
		{"v1.3.0", "v1.3.0", false},
		{"v1.3.1", "v1.3.0", false},
		{"v1.2.3", "v1.10.0", true}, // numeric, not lexical
		{"v1.10.0", "v1.9.9", false},
	} {
		if got := isNewer(tt.current, tt.latest); got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestPickAsset(t *testing.T) {
	assets := []asset{
		{Name: "jog_1.4.0_linux_amd64.tar.gz", URL: "u1"},
		{Name: "jog_1.4.0_linux_arm64.tar.gz", URL: "u2"},
		{Name: "jog_1.4.0_windows_amd64.zip", URL: "u3"},
		{Name: "checksums.txt", URL: "u4"},
	}
	bin, sums, err := pickAsset(assets, "v1.4.0", "linux", "arm64")
	if err != nil || bin.URL != "u2" || sums.URL != "u4" {
		t.Errorf("linux/arm64: bin=%v sums=%v err=%v", bin, sums, err)
	}
	bin, _, err = pickAsset(assets, "v1.4.0", "windows", "amd64")
	if err != nil || bin.URL != "u3" {
		t.Errorf("windows/amd64 should pick the zip: bin=%v err=%v", bin, err)
	}
	if _, _, err = pickAsset(assets, "v1.4.0", "windows", "arm64"); err == nil {
		t.Error("missing platform asset should error")
	}
	if _, _, err = pickAsset(assets[:3], "v1.4.0", "linux", "amd64"); err == nil {
		t.Error("missing checksums.txt should error")
	}
}

func TestParseChecksums(t *testing.T) {
	m := parseChecksums("abc123  jog_1.4.0_linux_amd64.tar.gz\ndef456  jog_1.4.0_windows_amd64.zip\n\nnot a checksum line at all\n")
	if m["jog_1.4.0_linux_amd64.tar.gz"] != "abc123" || m["jog_1.4.0_windows_amd64.zip"] != "def456" {
		t.Errorf("parseChecksums = %v", m)
	}
	if len(m) != 2 {
		t.Errorf("junk lines should be ignored, got %v", m)
	}
}

// tarball / zipball build a one-member release archive in memory.
func tarball(t *testing.T, member string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipball(t *testing.T, member string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		assetName, member string
		archive           []byte
	}{
		{"jog_1.4.0_linux_amd64.tar.gz", "jog", tarball(t, "jog", []byte("new-binary"))},
		{"jog_1.4.0_windows_amd64.zip", "jog.exe", zipball(t, "jog.exe", []byte("new-binary"))},
	} {
		src := filepath.Join(dir, tt.assetName)
		if err := os.WriteFile(src, tt.archive, 0o644); err != nil {
			t.Fatal(err)
		}
		dst := src + ".bin"
		if err := extractBinary(src, tt.assetName, tt.member, dst); err != nil {
			t.Fatalf("%s: %v", tt.assetName, err)
		}
		b, err := os.ReadFile(dst)
		if err != nil || string(b) != "new-binary" {
			t.Errorf("%s: extracted %q, %v", tt.assetName, b, err)
		}
		if err := extractBinary(src, tt.assetName, "nope", dst+"2"); err == nil {
			t.Errorf("%s: missing member should error", tt.assetName)
		}
	}
}

// serveRelease stands up a fake GitHub: the release listing points its
// download URLs back at the same server.
func serveRelease(t *testing.T, tag, assetName string, archive []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/tyler-johnson/jog/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q, "assets": [`+
			`{"name": %q, "browser_download_url": %q},`+
			`{"name": "checksums.txt", "browser_download_url": %q}]}`,
			tag, assetName, srv.URL+"/dl/"+assetName, srv.URL+"/dl/checksums.txt")
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, checksums)
	})
	mux.HandleFunc("/dl/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, srv *httptest.Server, exe, goos, version string) (*Updater, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	return &Updater{
		Client:  &http.Client{Timeout: 10 * time.Second},
		APIBase: srv.URL,
		Exe:     exe,
		GOOS:    goos,
		GOARCH:  "amd64",
		Version: version,
		Out:     &out,
		Err:     &errb,
	}, &out, &errb
}

// TestUpdateEndToEnd: the full flow against a fake GitHub — list, pick,
// verify, extract, swap — replaces the "installed" file's bytes. Runs
// both archive formats so the swap and extraction are exercised on
// whatever OS the test host is.
func TestUpdateEndToEnd(t *testing.T) {
	for _, tt := range []struct {
		goos, assetName, member string
		build                   func(*testing.T, string, []byte) []byte
	}{
		{"linux", "jog_1.4.0_linux_amd64.tar.gz", "jog", tarball},
		{"windows", "jog_1.4.0_windows_amd64.zip", "jog.exe", zipball},
	} {
		srv := serveRelease(t, "v1.4.0", tt.assetName, tt.build(t, tt.member, []byte("new-binary")))
		exe := filepath.Join(t.TempDir(), tt.member)
		if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		u, out, errb := testUpdater(t, srv, exe, tt.goos, "v1.3.0")
		if code := u.Run(); code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", tt.goos, code, errb)
		}
		if !strings.Contains(out.String(), "updated v1.3.0 → v1.4.0") {
			t.Errorf("%s: output %q", tt.goos, out)
		}
		b, err := os.ReadFile(exe)
		if err != nil || string(b) != "new-binary" {
			t.Errorf("%s: binary is %q, %v", tt.goos, b, err)
		}
		// Nothing but the binary (and possibly a parked .old) is left.
		entries, err := os.ReadDir(filepath.Dir(exe))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != tt.member && e.Name() != tt.member+".old" {
				t.Errorf("%s: leftover temp file %s", tt.goos, e.Name())
			}
		}
	}
}

func TestUpdateAlreadyCurrent(t *testing.T) {
	archive := tarball(t, "jog", []byte("same"))
	srv := serveRelease(t, "v1.3.0", "jog_1.3.0_linux_amd64.tar.gz", archive)
	exe := filepath.Join(t.TempDir(), "jog")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	u, out, errb := testUpdater(t, srv, exe, "linux", "v1.3.0")
	if code := u.Run(); code != 0 || !strings.Contains(out.String(), "already up to date") {
		t.Errorf("code=%d out=%q err=%q", code, out, errb)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Error("binary touched despite being current")
	}
}

func TestUpdateChecksumMismatch(t *testing.T) {
	archive := tarball(t, "jog", []byte("new-binary"))
	srv := serveRelease(t, "v1.4.0", "jog_1.4.0_linux_amd64.tar.gz", archive)
	// Re-point the archive download at different bytes than the
	// checksums were computed from.
	tampered := serveTampered(t, srv, "/dl/jog_1.4.0_linux_amd64.tar.gz", tarball(t, "jog", []byte("evil")))
	exe := filepath.Join(t.TempDir(), "jog")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	u, _, errb := testUpdater(t, tampered, exe, "linux", "v1.3.0")
	if code := u.Run(); code != 1 || !strings.Contains(errb.String(), "checksum mismatch") {
		t.Errorf("code=%d stderr=%q", code, errb)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Error("binary replaced despite checksum mismatch")
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

// serveTampered proxies srv but swaps one path's bytes.
func serveTampered(t *testing.T, srv *httptest.Server, path string, body []byte) *httptest.Server {
	t.Helper()
	var tampered *httptest.Server
	tampered = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			w.Write(body)
			return
		}
		resp, err := http.Get(srv.URL + r.URL.Path)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		b := new(bytes.Buffer)
		b.ReadFrom(resp.Body)
		// Release JSON carries absolute URLs to the origin server —
		// rewrite them so downloads flow back through the tamperer.
		w.Write(bytes.ReplaceAll(b.Bytes(), []byte(srv.URL), []byte(tampered.URL)))
	}))
	t.Cleanup(tampered.Close)
	return tampered
}
