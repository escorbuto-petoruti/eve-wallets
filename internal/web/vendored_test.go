package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestVendoredChartJS proves that the Chart.js asset named by the go:embed
// directive exists in the tree, is the library announced in its banner and
// still matches the SHA-256 recorded in static/VENDORED.md.
func TestVendoredChartJS(t *testing.T) {
	data, err := assets.ReadFile("static/chart.umd.min.js")
	if err != nil {
		t.Fatalf("embedded Chart.js is missing: %v", err)
	}
	banner := string(data)
	if len(banner) > 200 {
		banner = banner[:200]
	}
	if !strings.HasPrefix(banner, "/*!") || !strings.Contains(banner, "Chart.js v") {
		t.Fatalf("embedded file does not look like the Chart.js bundle")
	}

	record, err := os.ReadFile("static/VENDORED.md")
	if err != nil {
		t.Fatalf("read VENDORED.md: %v", err)
	}
	m := regexp.MustCompile(`SHA-256:\s*([0-9a-f]{64})`).FindSubmatch(record)
	if m == nil {
		t.Fatal("VENDORED.md does not record a SHA-256")
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != string(m[1]) {
		t.Fatalf("embedded Chart.js sha256 = %s, VENDORED.md records %s", got, m[1])
	}
}
