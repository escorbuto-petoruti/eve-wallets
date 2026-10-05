package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestShouldOpenBrowser(t *testing.T) {
	tests := []struct {
		name         string
		goos         string
		tty          bool
		open, noOpen bool
		addr         string
		want         bool
	}{
		{"windows with a terminal opens by default", "windows", true, false, false, "127.0.0.1:8088", true},
		{"windows without a terminal does not", "windows", false, false, false, "127.0.0.1:8088", false},
		{"linux default does not", "linux", true, false, false, "127.0.0.1:8088", false},
		{"darwin default does not", "darwin", true, false, false, "127.0.0.1:8088", false},
		{"--open forces it on linux", "linux", false, true, false, "127.0.0.1:8088", true},
		{"--no-open wins on windows", "windows", true, false, true, "127.0.0.1:8088", false},
		{"--open on windows without a terminal", "windows", false, true, false, "127.0.0.1:8088", true},
		{"non-loopback never opens", "windows", true, true, false, "0.0.0.0:8088", false},
		{"ipv6 loopback opens", "windows", true, false, false, "[::1]:8088", true},
		{"localhost opens", "windows", true, false, false, "localhost:8088", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldOpenBrowser(tt.goos, tt.tty, tt.open, tt.noOpen, tt.addr); got != tt.want {
				t.Errorf("shouldOpenBrowser = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBrowserURL(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8088": "http://localhost:8088",
		"[::1]:51234":    "http://localhost:51234",
		"localhost:9":    "http://localhost:9",
	} {
		if got := browserURL(addr); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestBrowserCommand(t *testing.T) {
	u := "http://localhost:8088"
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", u}},
		{"darwin", "open", []string{u}},
		{"linux", "xdg-open", []string{u}},
	}
	for _, tt := range tests {
		name, args := browserCommand(tt.goos, u)
		if name != tt.name || !reflect.DeepEqual(args, tt.args) {
			t.Errorf("browserCommand(%q) = %q %q, want %q %q", tt.goos, name, args, tt.name, tt.args)
		}
	}
}

func serveWithOpener(t *testing.T, goos string, tty bool, opener func(string) error, args ...string) (*harness, chan int) {
	t.Helper()
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	h.deps.goos = goos
	h.deps.isTerminal = func() bool { return tty }
	h.deps.openBrowser = opener
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	all := append([]string{"serve", "--no-collect", "--addr", "127.0.0.1:0"}, args...)
	go func() { done <- run(ctx, all, h.deps) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})
	return h, done
}

func TestServeOpensTheBrowserOnWindowsTerminal(t *testing.T) {
	urls := make(chan string, 1)
	h, _ := serveWithOpener(t, "windows", true, func(u string) error { urls <- u; return nil })
	ln := <-h.listeners
	select {
	case u := <-urls:
		if want := "http://localhost:" + listenPort(ln.Addr().String()); u != want {
			t.Errorf("opened %q, want %q", u, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("browser was not opened")
	}
}

func TestServeDoesNotOpenTheBrowserByDefaultOnLinux(t *testing.T) {
	called := make(chan string, 1)
	h, _ := serveWithOpener(t, "linux", true, func(u string) error { called <- u; return nil })
	<-h.listeners
	waitFor(t, "server up", func() bool { return strings.Contains(h.out.String(), "Serving on") })
	select {
	case u := <-called:
		t.Fatalf("opened %q, want no browser", u)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestServeOpenFlagForcesAndNoOpenDisables(t *testing.T) {
	urls := make(chan string, 1)
	h, _ := serveWithOpener(t, "linux", false, func(u string) error { urls <- u; return nil }, "--open")
	<-h.listeners
	select {
	case <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("--open did not open the browser")
	}

	called := make(chan string, 1)
	h2, _ := serveWithOpener(t, "windows", true, func(u string) error { called <- u; return nil }, "--no-open")
	<-h2.listeners
	waitFor(t, "server up", func() bool { return strings.Contains(h2.out.String(), "Serving on") })
	select {
	case u := <-called:
		t.Fatalf("--no-open opened %q", u)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestServeOpenFailureIsANotice(t *testing.T) {
	h, done := serveWithOpener(t, "windows", true, func(string) error { return errors.New("no handler") })
	<-h.listeners
	waitFor(t, "notice", func() bool { return strings.Contains(h.err.String(), "could not open the browser") })
	if !strings.Contains(h.err.String(), "no handler") {
		t.Errorf("stderr = %q", h.err.String())
	}
	select {
	case code := <-done:
		t.Fatalf("server stopped with %d after an opener failure", code)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestServeOpenAndNoOpenTogetherIsAUsageError(t *testing.T) {
	h := newHarness(t, nil)
	if got := run(context.Background(), []string{"serve", "--open", "--no-open"}, h.deps); got != 2 {
		t.Errorf("exit = %d, want 2", got)
	}
}
