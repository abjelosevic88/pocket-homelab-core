package appkit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// ModeReporter tells Core how each person actually connects to each Pocket
// app or service ("core", "direct" or "off"): Core can switch its own links
// but cannot see a URL and token pasted into the app. Report after each
// registration and right after a person changes a mode; one batched call
// for everybody. Report only: failures are logged, nothing depends on them.
type ModeReporter struct {
	Core *core.Client
	// Modes computes every person's effective mode at send time, so the last
	// report always carries the latest state.
	Modes func(ctx context.Context) ([]core.ConnMode, error)
	Log   *slog.Logger

	mu sync.Mutex
}

func (m *ModeReporter) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// Report sends the modes when the app is registered with Core. Serialized.
func (m *ModeReporter) Report(ctx context.Context) {
	if m == nil || !m.Core.Configured() || m.Core.Token() == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	modes, err := m.Modes(ctx)
	if err != nil {
		m.log().Warn("pocket core: reading connection modes failed", "err", err)
		return
	}
	if err := m.Core.ReportModes(ctx, modes); err != nil {
		m.log().Warn("pocket core: reporting connection modes failed", "err", err)
		return
	}
	m.log().Debug("pocket core: connection modes reported", "count", len(modes))
}

// Soon reports in the background, so saving settings never waits on Core.
// Safe to call from any handler or goroutine (where older code did
// `go s.reportModes(…)`); reports are serialized.
func (m *ModeReporter) Soon() {
	if m == nil || !m.Core.Configured() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		m.Report(ctx)
	}()
}

// Connection modes, chosen per person and per app in Settings.
const (
	ModeCore   = "core"
	ModeDirect = "direct"
	ModeOff    = "off"
)

// ConnMode is the mode in effect for a saved choice: the chosen one, else
// direct when a token (or password) was saved, else Pocket Core when the app
// is connected to one, else direct.
func ConnMode(c *core.Client, mode, secret string) string {
	switch mode {
	case ModeCore, ModeDirect, ModeOff:
		return mode
	}
	if secret != "" || !c.Configured() {
		return ModeDirect
	}
	return ModeCore
}
