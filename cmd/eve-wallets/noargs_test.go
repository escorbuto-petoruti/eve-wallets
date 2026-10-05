package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// With no arguments Windows runs serve (a double-click gives no arguments and
// the console would otherwise flash and close); everywhere else it prints the
// usage and exits 2.
func TestNoArgumentsDependOnTheOS(t *testing.T) {
	tests := []struct {
		goos  string
		serve bool
	}{
		{"windows", true},
		{"linux", false},
		{"darwin", false},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			h := newHarness(t, map[string]string{"HOME": "/home/u"})
			h.deps.goos = tt.goos
			gotAddr := make(chan string, 1)
			inner := h.deps.listen
			h.deps.listen = func(network, addr string) (net.Listener, error) {
				gotAddr <- addr
				return inner(network, "127.0.0.1:0") // never bind the real default port
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() { done <- run(ctx, nil, h.deps) }()

			if !tt.serve {
				select {
				case code := <-done:
					if code != 2 || !strings.Contains(h.err.String(), "Usage:") {
						t.Errorf("exit = %d, stderr = %q; want 2 and the usage", code, h.err.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("run did not return")
				}
				select {
				case a := <-gotAddr:
					t.Errorf("listened on %q, want no server", a)
				default:
				}
				return
			}
			select {
			case a := <-gotAddr:
				if a != defaultAddr {
					t.Errorf("listened on %q, want the default %q", a, defaultAddr)
				}
			case code := <-done:
				t.Fatalf("run exited with %d: %s", code, h.err.String())
			case <-time.After(5 * time.Second):
				t.Fatal("serve did not start")
			}
			waitFor(t, "banner", func() bool { return strings.Contains(h.out.String(), "Quit button") })
			out := h.out.String()
			for _, want := range []string{"Close this window", "Ctrl+C", "Quit button"} {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q: %q", want, out)
				}
			}
			cancel()
			if code := <-done; code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
		})
	}
}

// POST /api/shutdown stops serve the same way Ctrl+C does: the context passed
// to run is never cancelled here, yet serve returns 0 and stops listening.
func TestServeShutsDownThroughTheQuitEndpoint(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, _, done := startServe(t, h, "--no-collect")
	c := signIn(t, base)

	req, err := http.NewRequest(http.MethodPost, base+"/api/shutdown", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", base)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want 202", resp.StatusCode)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d; stderr = %q", code, h.err.String())
		}
	case <-time.After(15 * time.Second): // Shutdown may wait out a speculative idle connection
		t.Fatal("serve did not stop")
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
}
