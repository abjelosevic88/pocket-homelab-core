package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/remote"
)

// runRelay runs a self-hosted remote-access relay (docs/remote.md). Put it
// behind a TLS proxy that answers for the whole address pattern.
func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	listen := fs.String("listen", envOr("RELAY_LISTEN", ":8080"), "address to listen on")
	secret := fs.String("secret", os.Getenv("RELAY_SECRET"), "the relay secret from Core → Remote access (or RELAY_SECRET)")
	portal := fs.String("portal", os.Getenv("RELAY_PORTAL"), "the launcher address, e.g. https://pocket.remote.example.org (or RELAY_PORTAL)")
	trust := fs.Bool("trust-proxy", os.Getenv("RELAY_TRUST_PROXY") != "false", "take client addresses from X-Forwarded-For")
	_ = fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *secret == "" {
		fmt.Fprintln(os.Stderr, "relay: set -secret or RELAY_SECRET (Core → Remote access → Self-hosted relay shows it)")
		os.Exit(2)
	}
	rl := &remote.Relay{Secret: *secret, Portal: *portal, TrustProxy: *trust, Log: log}
	srv := &http.Server{Addr: *listen, Handler: rl, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("pocket relay listening", "addr", *listen, "portal", *portal)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("relay stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
