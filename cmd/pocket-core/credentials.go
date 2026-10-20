package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/server"
)

// runCredentials: pocket-core credentials claim-box [-email USER] [-dry-run]
//
// Apps reaching third-party services through Core get only the person's own
// credential, never the box-wide one. On a box where the box-wide keys are
// really the owner's accounts, this copies them into the owner's own
// credentials once, so nothing stops working. Stop Core first.
func runCredentials(args []string) {
	if len(args) == 0 || args[0] != "claim-box" {
		fmt.Fprintln(os.Stderr, "usage: pocket-core credentials claim-box [-email USER] [-dry-run]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("claim-box", flag.ExitOnError)
	email := fs.String("email", "", "whose credentials (default: the first user)")
	dry := fs.Bool("dry-run", false, "only list what would be copied")
	_ = fs.Parse(args[1:])
	_, log, st := mustLoad()
	defer st.Close()
	ctx := context.Background()
	u, err := firstUser(ctx, st, *email)
	if err != nil {
		log.Error("claim-box: user lookup failed", "err", err)
		os.Exit(1)
	}
	done, err := server.ClaimBoxCredentials(ctx, st, u, *dry)
	if err != nil {
		log.Error("claim-box", "err", err)
		os.Exit(1)
	}
	verb := "Copied"
	if *dry {
		verb = "Would copy"
	}
	if len(done) == 0 {
		fmt.Printf("Nothing to copy: %s already has their own credentials for every service with a box-wide key.\n", u.Email)
		return
	}
	fmt.Printf("%s the box-wide credentials into %s's own for: %s\n", verb, u.Email, strings.Join(done, ", "))
}
