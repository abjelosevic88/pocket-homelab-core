package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
)

// runInstaller starts the installer agent: the host-side helper behind the
// Install and Remove buttons. It runs on the host (not in Core's container)
// as the user who owns the app folders, with access to Docker.
func runInstaller(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println(`pocket-core installer — install and remove Pocket apps for Core (see docs/installer.md)

Runs on the Docker host, answers Core on INSTALLER_SOCKET. Settings (environment):
  INSTALLER_SOCKET         unix socket, mounted into Core's container (required)
  INSTALLER_TOKEN[_FILE]   shared secret Core sends, 24+ characters (required)
  INSTALLER_PUBLIC_URL     address of a new app, e.g. https://{slug}.example.com (required)
  INSTALLER_GITHUB_OWNER   GitHub account apps are released from (abjelosevic88)
  INSTALLER_REPO_PREFIX    only repos starting with this (pocket-homelab-)
  INSTALLER_APPS_DIR       app folders go here as pocket-<slug> ($HOME)
  INSTALLER_ARCHIVE_DIR    removed apps' folders go here ($APPS_DIR/pocket-archive)
  INSTALLER_CORE_URL       Core as app containers reach it (http://host.docker.internal:8179)
  INSTALLER_PORTS          host ports for new apps (8300-8399)
  INSTALLER_CADDYFILE      Caddyfile to add/remove sites in; empty = leave the proxy alone
  INSTALLER_CADDY_IMPORTS  snippets each new site imports, comma separated (tls_cf)
  INSTALLER_CADDY_CONTAINER, INSTALLER_CADDY_PATH, INSTALLER_CADDY_UPSTREAM
  INSTALLER_LEFTOVER_FILES files to search for a removed app's address
  INSTALLER_PROTECTED      slugs never removed (core always)`)
		return
	}
	cfg, err := installer.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "installer:", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := installer.NewAgent(cfg, log).Serve(ctx); err != nil {
		log.Error("installer", "err", err)
		os.Exit(1)
	}
}
