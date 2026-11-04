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
// Install, Update and Remove buttons. It runs on the host as the user who
// owns the app folders, with access to Docker — or in its own container
// (the pocket-homelab-core-installer image) with the Docker socket.
func runInstaller(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println(`pocket-core installer — install and remove Pocket apps for Core (see docs/installer.md)

Runs on the Docker host, answers Core on INSTALLER_SOCKET. Settings (environment):
  INSTALLER_SOCKET         unix socket, mounted into Core's container (required)
  INSTALLER_TOKEN[_FILE]   shared secret Core sends, 24+ characters (required)
  INSTALLER_PUBLIC_URL     address of a new app: https://{slug}.example.com, or
                           http://192.168.1.20:{port} without a domain (required)
  INSTALLER_CATALOG_URL    signed app catalog(s), comma separated (the official one);
                           "github" = read the repos straight from GitHub instead
  INSTALLER_CATALOG_KEYS   extra trusted catalog keys, <id>:<base64>, comma separated
  INSTALLER_GITHUB_OWNER   GitHub account apps are released from (abjelosevic88)
  INSTALLER_REPO_PREFIX    only repos starting with this (pocket-homelab-)
  INSTALLER_DOCKER_GID     the docker group's id, when the agent runs in a container
  INSTALLER_APP_OWNER      run as root: new apps' data folders go to this uid:gid (1000:1000)
  INSTALLER_FILES_OWNER    run as root: app folders, compose files, .env and the socket go to this uid:gid
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
