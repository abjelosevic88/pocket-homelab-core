// Command pocket-core is the hub of a Pocket Box: the registry Pocket apps
// announce themselves to, the event bus between them, the OpenID Connect
// provider they log in with, and the pairing service that hands a phone
// every service in one QR code. One binary, one SQLite file, one volume.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/server"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/abjelosevic88/pocket-homelab-core/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("pocket-core", version)
			return
		case "backup":
			runBackup(os.Args[2:])
			return
		case "reset-password":
			runResetPassword(os.Args[2:])
			return
		case "registration-key":
			runRegistrationKey()
			return
		case "pair":
			runPair(os.Args[2:])
			return
		case "help", "--help", "-h":
			fmt.Println(`pocket-core — the hub of a Pocket Box: app registry, event bus, OIDC provider, device pairing

Usage:
  pocket-core                        start the server (default)
  pocket-core backup [-o FILE]       write a consistent SQLite backup
  pocket-core reset-password [-email USER] [-password NEW]
                                     set a user's password (default: first user, random password printed)
  pocket-core registration-key       print the key apps use to register themselves
  pocket-core pair [-email USER]     print a pairing code (and its URL) for a user, from the console
  pocket-core version                print version

Configuration is read from environment variables, see docs/configuration.md.`)
			return
		}
	}
	serve()
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func mustLoad() (*config.Config, *slog.Logger, *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	log := newLogger(cfg)
	box, err := crypto.LoadBox(cfg.SecretPath)
	if err != nil {
		log.Error("secret key", "path", cfg.SecretPath, "err", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DBPath, box, log)
	if err != nil {
		log.Error("open database", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	return cfg, log, st
}

func serve() {
	server.Version = version
	cfg, log, st := mustLoad()
	defer st.Close()

	var webFS fs.FS
	if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if f, err := sub.Open("index.html"); err == nil {
			f.Close()
			webFS = sub
		}
	}
	if webFS == nil {
		log.Warn("web UI not embedded; API only (run `make web` before building)")
	}

	srv := server.New(cfg, st, log, webFS)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Bootstrap(ctx); err != nil {
		log.Error("bootstrap", "err", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Info("pocket-core started", "version", version, "addr", cfg.Addr(), "base_url", cfg.BaseURL, "issuer", cfg.OIDCIssuer, "db", cfg.DBPath, "auth_mode", cfg.AuthMode)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer c2()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func runBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("o", "", "output file (default: DATA_DIR/backups/pocket-core-<timestamp>.db)")
	_ = fs.Parse(args)
	cfg, log, st := mustLoad()
	defer st.Close()
	dst := *out
	if dst == "" {
		_ = os.MkdirAll(cfg.DataDir+"/backups", 0o755)
		dst = fmt.Sprintf("%s/backups/pocket-core-%s.db", cfg.DataDir, time.Now().Format("20060102-150405"))
	}
	if err := st.Backup(context.Background(), dst); err != nil {
		log.Error("backup failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(dst)
}

func firstUser(ctx context.Context, st *store.Store, email string) (*store.User, error) {
	if email != "" {
		return st.GetUserByEmail(ctx, email)
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, errors.New("no users found; finish setup in the browser first")
	}
	return &users[0], nil
}

func runResetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	email := fs.String("email", "", "user email (default: the first user)")
	password := fs.String("password", "", "new password (default: generate a random one)")
	_ = fs.Parse(args)
	_, log, st := mustLoad()
	defer st.Close()
	ctx := context.Background()
	u, err := firstUser(ctx, st, *email)
	if err != nil {
		log.Error("reset-password: user lookup failed", "err", err)
		os.Exit(1)
	}
	pw := *password
	if pw == "" {
		const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			log.Error("reset-password: random", "err", err)
			os.Exit(1)
		}
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		pw = string(b)
	}
	if len(pw) < 8 {
		log.Error("reset-password: password must be at least 8 characters")
		os.Exit(1)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		log.Error("reset-password: hash", "err", err)
		os.Exit(1)
	}
	u.PasswordHash = string(h)
	if err := st.UpdateUser(ctx, u); err != nil {
		log.Error("reset-password: save", "err", err)
		os.Exit(1)
	}
	if *password == "" {
		fmt.Printf("Password for %s reset.\nTemporary password: %s\nChange it under Settings → Profile after logging in.\n", u.Email, pw)
	} else {
		fmt.Printf("Password for %s reset.\n", u.Email)
	}
}

func runRegistrationKey() {
	_, log, st := mustLoad()
	defer st.Close()
	s, err := st.GetSettings(context.Background())
	if err != nil {
		log.Error("settings", "err", err)
		os.Exit(1)
	}
	if s.RegistrationKey == "" {
		fmt.Println("no registration key yet — start the server once")
		os.Exit(1)
	}
	fmt.Println(s.RegistrationKey)
}

// runPair prints a pairing code from the console: handy on a box with a
// screen, or over SSH, before the admin has ever logged in to the UI.
func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	email := fs.String("email", "", "user to pair the device to (default: the first user)")
	_ = fs.Parse(args)
	cfg, log, st := mustLoad()
	defer st.Close()
	ctx := context.Background()
	u, err := firstUser(ctx, st, *email)
	if err != nil {
		log.Error("pair", "err", err)
		os.Exit(1)
	}
	p, err := st.CreatePairing(ctx, u.ID, cfg.PairTTL)
	if err != nil {
		log.Error("pair", "err", err)
		os.Exit(1)
	}
	fmt.Printf("Pairing code for %s (valid %s):\n\n    %s-%s\n\nOpen Pocket Homelab → Pair with a box, or scan:\n    %s/api/v1/pair/%s/qr.png\n", u.Email, cfg.PairTTL, p.Code[:4], p.Code[4:], cfg.BaseURL, p.Code)
}
