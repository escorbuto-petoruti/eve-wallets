package main

import (
	"context"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	old := version
	version = "1.2.3"
	t.Cleanup(func() { version = old })
	for _, arg := range []string{"version", "--version"} {
		h := newHarness(t, nil)
		if code := run(context.Background(), []string{arg}, h.deps); code != 0 {
			t.Errorf("%s: exit %d, stderr %q", arg, code, h.err.String())
		}
		if got := h.out.String(); got != "eve-wallets 1.2.3\n" {
			t.Errorf("%s: stdout = %q", arg, got)
		}
	}
}
