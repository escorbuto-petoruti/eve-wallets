// Package update replaces the running eve-wallets binary with the latest
// GitHub release. It downloads the archive for the platform and the release's
// checksums.txt, verifies the SHA-256 and swaps the executable atomically. It
// never touches the database: the new binary backs it up on its first start.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is the GitHub API root of the project's releases.
const DefaultAPIBase = "https://api.github.com/repos/escorbuto-petoruti/eve-wallets"

const (
	binaryName   = "eve-wallets"
	maxBinary    = 200 << 20 // size cap for the extracted binary and the archive
	maxChecksums = 1 << 20
	maxMetadata  = 4 << 20
)

// Options configures Run.
type Options struct {
	APIBase   string       // default DefaultAPIBase
	Client    *http.Client // default: a client with a 5 minute timeout
	Version   string       // the running version, "dev" for a source build
	OS, Arch  string       // platform of the archive to fetch
	ExePath   string       // executable to replace; default: the running one
	Force     bool         // allow a dev build to update
	CheckOnly bool         // report only, change nothing
	Stdout    io.Writer
}

type releaseInfo struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Run checks the latest release and, unless CheckOnly, applies it.
func Run(ctx context.Context, o Options) error {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.APIBase == "" {
		o.APIBase = DefaultAPIBase
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.OS != "linux" && o.OS != "darwin" {
		return fmt.Errorf("self-update is not supported on %s: download the release archive and replace the binary by hand", o.OS)
	}
	dev := o.Version == "dev"
	if dev && !o.Force && !o.CheckOnly {
		return errors.New("this is a dev build (built from source): update would replace it with a release; pass --force to do it anyway")
	}

	rel, err := latestRelease(ctx, o)
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	if dev {
		fmt.Fprintf(o.Stdout, "running a dev build; latest release is %s\n", latest)
		if o.CheckOnly {
			return nil
		}
	} else {
		cmp, err := compareVersions(o.Version, latest)
		if err != nil {
			return err
		}
		switch {
		case cmp >= 0:
			fmt.Fprintf(o.Stdout, "eve-wallets %s is up to date (latest release: %s)\n", o.Version, latest)
			return nil
		case o.CheckOnly:
			fmt.Fprintf(o.Stdout, "update available: %s -> %s (run `eve-wallets update`)\n", o.Version, latest)
			return nil
		}
	}

	archiveName := fmt.Sprintf("eve-wallets_%s_%s_%s.tar.gz", latest, o.OS, o.Arch)
	archiveURL, sumsURL := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case archiveName:
			archiveURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s and checksums.txt", rel.Tag, archiveName)
	}

	sums, err := download(ctx, o, sumsURL, maxChecksums)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	want, err := checksumFor(sums, archiveName)
	if err != nil {
		return err
	}
	archive, err := download(ctx, o, archiveURL, maxBinary)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", archiveName)
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return err
	}

	exe := o.ExePath
	if exe == "" {
		if exe, err = currentExecutable(); err != nil {
			return err
		}
	}
	if err := replaceExecutable(exe, bin); err != nil {
		return err
	}
	prev := o.Version
	fmt.Fprintf(o.Stdout, "updated %s: %s -> %s\nprevious binary kept as %s\nrestart eve-wallets (for the systemd service: systemctl --user restart eve-wallets)\n",
		exe, prev, latest, exe+".bak-prev")
	return nil
}

func currentExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running executable: %w", err)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

func get(ctx context.Context, o Options, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "eve-wallets/"+o.Version+" (update)")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func download(ctx context.Context, o Options, url string, limit int64) ([]byte, error) {
	resp, err := get(ctx, o, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return b, nil
}

func latestRelease(ctx context.Context, o Options) (*releaseInfo, error) {
	b, err := download(ctx, o, strings.TrimRight(o.APIBase, "/")+"/releases/latest", maxMetadata)
	if err != nil {
		return nil, fmt.Errorf("look up the latest release: %w", err)
	}
	var rel releaseInfo
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, fmt.Errorf("decode the latest release: %w", err)
	}
	if rel.Tag == "" {
		return nil, errors.New("the latest release has no tag")
	}
	return &rel, nil
}

// checksumFor finds the SHA-256 of name in sha256sum formatted text.
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			h := strings.ToLower(fields[0])
			if len(h) != 64 {
				return "", fmt.Errorf("checksums.txt: malformed hash for %s", name)
			}
			return h, nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// extractBinary returns the eve-wallets file at the top of the tar.gz archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive does not contain %s", binaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || strings.TrimPrefix(h.Name, "./") != binaryName {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, fmt.Errorf("read %s from the archive: %w", binaryName, err)
		}
		if int64(len(b)) > maxBinary || len(b) == 0 {
			return nil, fmt.Errorf("%s in the archive has an invalid size", binaryName)
		}
		return b, nil
	}
}

// replaceExecutable writes bin next to exe, keeps the current file as
// exe.bak-prev and renames the new file over exe.
func replaceExecutable(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".eve-wallets-new-*")
	if err != nil {
		return fmt.Errorf("write next to %s (is the directory writable?): %w", exe, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod the new binary: %w", err)
	}
	if err := keepPrevious(exe); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// keepPrevious links (or, failing that, copies) exe to exe.bak-prev.
func keepPrevious(exe string) error {
	prev := exe + ".bak-prev"
	if _, err := os.Lstat(exe); err != nil {
		return nil // nothing to keep
	}
	_ = os.Remove(prev)
	if err := os.Link(exe, prev); err == nil {
		return nil
	}
	src, err := os.Open(exe)
	if err != nil {
		return fmt.Errorf("keep the previous binary: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(prev, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("keep the previous binary: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("keep the previous binary: %w", err)
	}
	return dst.Close()
}

// compareVersions compares dotted numeric versions ("1.2.3", optional leading
// "v"). A suffix after "-" (a prerelease or a git describe) sorts before the
// same version without one. It returns -1, 0 or 1.
func compareVersions(a, b string) (int, error) {
	pa, sa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, sb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case sa == sb:
		return 0, nil
	case sa && !sb:
		return -1, nil
	case !sa && sb:
		return 1, nil
	}
	return 0, nil
}

func parseVersion(v string) (parts [3]int, suffix bool, err error) {
	core := strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core, suffix = core[:i], true
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return parts, false, fmt.Errorf("not a version: %q (want X.Y.Z)", v)
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, false, fmt.Errorf("not a version: %q (want X.Y.Z)", v)
		}
		parts[i] = n
	}
	return parts, suffix, nil
}
