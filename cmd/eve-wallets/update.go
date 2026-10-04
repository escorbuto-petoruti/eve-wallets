package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/escorbuto-petoruti/eve-wallets/internal/update"
)

const windowsUpdateMsg = `eve-wallets: self-update is not available on Windows. To update, stop eve-wallets and run the installer again in PowerShell:
  irm https://raw.githubusercontent.com/escorbuto-petoruti/eve-wallets/main/install.ps1 | iex
Or update by hand:
  1. download eve-wallets_<version>_windows_amd64.zip and checksums.txt from the latest GitHub release
  2. check the hash: Get-FileHash <zip> -Algorithm SHA256 (compare with checksums.txt)
  3. stop eve-wallets, replace eve-wallets.exe with the one in the zip, start it again
`

// runUpdate implements `eve-wallets update [--check] [--force]`.
func runUpdate(ctx context.Context, args []string, d deps) int {
	fset := newFlagSet("update", d)
	check := fset.Bool("check", false, "only report whether a newer release exists")
	force := fset.Bool("force", false, "update even a dev build")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	if fset.NArg() != 0 {
		fmt.Fprintf(d.stderr, "eve-wallets: update takes no arguments\n")
		return 2
	}
	if runtime.GOOS == "windows" {
		fmt.Fprint(d.stderr, windowsUpdateMsg)
		return 1
	}
	err := update.Run(ctx, update.Options{
		APIBase:   d.getenv("EVE_WALLETS_UPDATE_API"),
		Version:   version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Force:     *force,
		CheckOnly: *check,
		Stdout:    d.stdout,
	})
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: update: %v\n", err)
		return 1
	}
	return 0
}
