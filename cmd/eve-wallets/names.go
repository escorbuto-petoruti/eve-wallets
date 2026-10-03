package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// openStoreOnly resolves the database path and opens the store for a command
// that needs no collector. It returns (nil, code, true) when the command must
// stop.
func openStoreOnly(dbFlag *string, d deps) (*store.Store, int, bool) {
	path, err := resolveDBPath(*dbFlag, d.getenv)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return nil, 1, true
	}
	st, err := d.openStore(path)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return nil, 1, true
	}
	return st, 0, false
}

// runWallets lists the wallets with their displayed names. It needs neither
// the network nor eve-auth.
func runWallets(ctx context.Context, args []string, d deps) int {
	fset := newFlagSet("wallets", d)
	dbFlag := fset.String("db", "", "database path")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	if fset.NArg() != 0 {
		fmt.Fprint(d.stderr, usage)
		return 2
	}
	st, code, stop := openStoreOnly(dbFlag, d)
	if stop {
		return code
	}
	defer st.Close()

	wallets, err := st.Wallets(ctx)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	if len(wallets) == 0 {
		fmt.Fprintln(d.stdout, "No wallets yet: run `eve-wallets collect` first.")
		return 0
	}
	tw := tabwriter.NewWriter(d.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tOWNER\tDIVISION\tNAME\tSOURCE")
	for _, w := range wallets {
		division := ""
		if w.Kind == store.KindCorporation {
			division = strconv.Itoa(w.Division)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", w.ID, w.Kind, w.OwnerName, division, w.DisplayName(), w.NameSource())
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	return 0
}

// runLabel sets or clears the user label of one wallet.
func runLabel(ctx context.Context, args []string, d deps) int {
	fset := newFlagSet("label", d)
	dbFlag := fset.String("db", "", "database path")
	clearLabel := fset.Bool("clear", false, "remove the label")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	rest := fset.Args()
	if len(rest) == 0 || (*clearLabel && len(rest) != 1) || (!*clearLabel && len(rest) < 2) {
		fmt.Fprint(d.stderr, usage)
		return 2
	}
	id, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(d.stderr, "eve-wallets: invalid wallet id %q: it must be a positive number (see `eve-wallets wallets`)\n", rest[0])
		return 1
	}
	st, code, stop := openStoreOnly(dbFlag, d)
	if stop {
		return code
	}
	defer st.Close()

	if *clearLabel {
		err = st.ClearLabel(ctx, id)
	} else {
		err = st.SetLabel(ctx, id, strings.Join(rest[1:], " "))
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		fmt.Fprintf(d.stderr, "eve-wallets: no wallet with id %d (see `eve-wallets wallets`)\n", id)
		return 1
	case errors.Is(err, store.ErrInvalidName):
		fmt.Fprintln(d.stderr, "eve-wallets: invalid name: it must be 1-64 characters, no control characters")
		return 1
	case err != nil:
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	wallets, err := st.Wallets(ctx)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	for _, w := range wallets {
		if w.ID == id {
			fmt.Fprintf(d.stdout, "Wallet %d (%s): name is now %q (%s)\n", w.ID, w.OwnerName, w.DisplayName(), w.NameSource())
			return 0
		}
	}
	fmt.Fprintf(d.stderr, "eve-wallets: no wallet with id %d\n", id)
	return 1
}
