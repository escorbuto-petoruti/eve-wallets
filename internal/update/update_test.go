package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release describes the fake release a test server serves.
type release struct {
	tag      string
	files    map[string]string // archive entries (name -> content)
	corrupt  bool              // serve checksums that do not match the archive
	noAssets bool
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serve(t *testing.T, r release, osName, arch string) *httptest.Server {
	t.Helper()
	ver := strings.TrimPrefix(r.tag, "v")
	archiveName := fmt.Sprintf("eve-wallets_%s_%s_%s.tar.gz", ver, osName, arch)
	archive := tarGz(t, r.files)
	sum := sha256.Sum256(archive)
	hash := hex.EncodeToString(sum[:])
	if r.corrupt {
		hash = strings.Repeat("0", 64)
	}
	checksums := fmt.Sprintf("%s  eve-wallets_%s_other_arch.tar.gz\n%s  %s\n", strings.Repeat("a", 64), ver, hash, archiveName)

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("User-Agent") == "" {
			t.Error("the GitHub API needs a User-Agent")
		}
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		out := struct {
			Tag    string  `json:"tag_name"`
			Assets []asset `json:"assets"`
		}{Tag: r.tag}
		if !r.noAssets {
			out.Assets = []asset{
				{archiveName, srv.URL + "/dl/" + archiveName},
				{"checksums.txt", srv.URL + "/dl/checksums.txt"},
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/dl/"+archiveName, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(checksums)) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func goodFiles() map[string]string {
	return map[string]string{"eve-wallets": "NEW-BINARY", "LICENSE": "mit", "README.md": "readme"}
}

// installed writes a fake current executable and returns its path.
func installed(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "eve-wallets")
	if err := os.WriteFile(p, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(srv *httptest.Server, exe, current string, out *bytes.Buffer) Options {
	return Options{APIBase: srv.URL, Version: current, OS: "linux", Arch: "amd64", ExePath: exe, Stdout: out}
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateAppliesNewerRelease(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles()}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	if err := Run(context.Background(), opts(srv, exe, "1.2.0", &out)); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if got := content(t, exe); got != "NEW-BINARY" {
		t.Errorf("executable = %q, want the new binary", got)
	}
	if got := content(t, exe+".bak-prev"); got != "OLD-BINARY" {
		t.Errorf("bak-prev = %q, want the old binary", got)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}
	for _, want := range []string{"1.2.0", "1.3.0", "restart"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".eve-wallets*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestUpdateSameVersionIsNoop(t *testing.T) {
	srv := serve(t, release{tag: "v1.2.0", files: goodFiles()}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	if err := Run(context.Background(), opts(srv, exe, "1.2.0", &out)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("executable changed on a same-version update")
	}
	if _, err := os.Stat(exe + ".bak-prev"); err == nil {
		t.Error("bak-prev created on a no-op")
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("output = %q", out.String())
	}
}

func TestUpdateOlderLatestIsNoop(t *testing.T) {
	srv := serve(t, release{tag: "v1.0.0", files: goodFiles()}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	if err := Run(context.Background(), opts(srv, exe, "1.2.0", &out)); err != nil {
		t.Fatal(err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("downgraded the executable")
	}
}

func TestUpdateChecksumMismatchLeavesBinaryUntouched(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles(), corrupt: true}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	err := Run(context.Background(), opts(srv, exe, "1.2.0", &out))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("executable changed despite the checksum mismatch")
	}
	if _, err := os.Stat(exe + ".bak-prev"); err == nil {
		t.Error("bak-prev created on a failed update")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".eve-wallets*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestUpdateDevBuildRefusesUnlessForced(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles()}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	err := Run(context.Background(), opts(srv, exe, "dev", &out))
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal that mentions --force", err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("a dev build was updated without --force")
	}

	o := opts(srv, exe, "dev", &out)
	o.Force = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if content(t, exe) != "NEW-BINARY" {
		t.Error("forced update did not apply")
	}
}

func TestUpdateArchiveMissingBinaryAborts(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: map[string]string{"LICENSE": "mit"}}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	err := Run(context.Background(), opts(srv, exe, "1.2.0", &out))
	if err == nil || !strings.Contains(err.Error(), "eve-wallets") {
		t.Fatalf("err = %v, want a missing binary error", err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("executable changed")
	}
}

func TestUpdateCheckOnlyReportsAndWritesNothing(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles()}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	o := opts(srv, exe, "1.2.0", &out)
	o.CheckOnly = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("--check modified the executable")
	}
	if !strings.Contains(out.String(), "1.3.0") {
		t.Errorf("output = %q", out.String())
	}
}

func TestUpdateMissingAssetsFails(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles(), noAssets: true}, "linux", "amd64")
	exe := installed(t)
	var out bytes.Buffer
	if err := Run(context.Background(), opts(srv, exe, "1.2.0", &out)); err == nil {
		t.Fatal("want an error when the release has no asset for this platform")
	}
	if content(t, exe) != "OLD-BINARY" {
		t.Error("executable changed")
	}
}

func TestUpdateUnsupportedPlatform(t *testing.T) {
	srv := serve(t, release{tag: "v1.3.0", files: goodFiles()}, "linux", "amd64")
	var out bytes.Buffer
	o := opts(srv, installed(t), "1.2.0", &out)
	o.OS = "windows"
	if err := Run(context.Background(), o); err == nil {
		t.Fatal("want an error on windows")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.2.0", 0}, {"1.2.0", "1.10.0", -1}, {"2.0.0", "1.99.99", 1},
		{"v1.2.0", "1.2.0", 0}, {"1.2.0-rc1", "1.2.0", -1}, {"1.2.0", "1.2.0-3-gabc", 1},
	}
	for _, c := range cases {
		got, err := compareVersions(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := compareVersions("banana", "1.0.0"); err == nil {
		t.Error("want an error for a non-version")
	}
}
