package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func latestServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","assets":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestUpdateCheckReportsNewerRelease(t *testing.T) {
	setVersion(t, "1.0.0")
	srv := latestServer(t, "v1.1.0")
	h := newHarness(t, map[string]string{"EVE_WALLETS_UPDATE_API": srv.URL})
	if code := run(context.Background(), []string{"update", "--check"}, h.deps); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, h.err.String())
	}
	if !strings.Contains(h.out.String(), "1.1.0") {
		t.Errorf("stdout = %q", h.out.String())
	}
}

func TestUpdateDevBuildRefusesWithoutForce(t *testing.T) {
	setVersion(t, "dev")
	srv := latestServer(t, "v1.1.0")
	h := newHarness(t, map[string]string{"EVE_WALLETS_UPDATE_API": srv.URL})
	if code := run(context.Background(), []string{"update"}, h.deps); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(h.err.String(), "--force") {
		t.Errorf("stderr = %q", h.err.String())
	}
}

func TestUpdateRejectsBadFlagAndArgs(t *testing.T) {
	h := newHarness(t, nil)
	if code := run(context.Background(), []string{"update", "--nope"}, h.deps); code != 2 {
		t.Errorf("bad flag: exit %d, want 2", code)
	}
	if code := run(context.Background(), []string{"update", "extra"}, h.deps); code != 2 {
		t.Errorf("extra arg: exit %d, want 2", code)
	}
}
