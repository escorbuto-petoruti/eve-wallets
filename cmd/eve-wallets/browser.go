package main

import (
	"os"
	"os/exec"
)

// shouldOpenBrowser decides whether serve opens the page. --no-open always
// wins and a non-loopback address never opens. Otherwise --open forces it and
// the default is on only for Windows with a terminal on stdout, so a service
// or scheduled task never launches a browser.
func shouldOpenBrowser(goos string, tty, open, noOpen bool, addr string) bool {
	if noOpen || checkLoopbackAddr(addr) != nil {
		return false
	}
	return open || (goos == "windows" && tty)
}

// browserURL is the page address for the real bound listen address.
func browserURL(boundAddr string) string {
	return "http://localhost:" + listenPort(boundAddr)
}

// browserCommand returns the program and arguments that open url. The url is
// always its own argument, never part of a shell string.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	default:
		return "xdg-open", []string{url}
	}
}

// startBrowser launches the system opener without waiting for the browser.
func startBrowser(goos, url string) error {
	name, args := browserCommand(goos, url)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// stdoutIsTerminal reports whether stdout is a character device (a console).
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
