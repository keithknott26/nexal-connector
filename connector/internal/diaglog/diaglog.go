// Package diaglog is the connector's diagnostic mode: a log level the owner can
// raise to Debug while the agent runs, with no restart, and a separate,
// size-capped diagnostics log that is written only while the mode is on.
//
// The switch is a flag file next to config.json (diagnostics.enabled). The Mac
// app sets it through `nexal diagnostics --on|--off`, the same bounded CLI seam
// it uses for every other command, and the running agent notices within a few
// seconds. Nothing here changes what is logged, only how much: every record
// still follows the agent's rule of never carrying tokens, setup keys,
// credentials, passwords or private keys.
package diaglog

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// FlagName is the file whose presence turns diagnostic mode on.
	FlagName = "diagnostics.enabled"
	// FileName is the diagnostics log written while the mode is on.
	FileName = "diagnostics.log"
	// AgentLogName is the agent's stderr log, kept by the Mac app.
	AgentLogName = "agent.log"
	// AppLogName is the Mac app's own diagnostic event log.
	AppLogName = "app.log"
	// MaxFileBytes caps diagnostics.log; one rotation (diagnostics.log.1) is kept.
	MaxFileBytes = 5 << 20
	// PollInterval is how often the running agent checks the flag file.
	PollInterval = 3 * time.Second
)

// FlagPath is the diagnostic-mode flag for the configuration at configPath.
func FlagPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), FlagName)
}

// Enabled reports whether diagnostic mode is switched on for configPath.
func Enabled(configPath string) bool {
	_, err := os.Stat(FlagPath(configPath))
	return err == nil
}

// SetEnabled creates or removes the flag file. Idempotent.
func SetEnabled(configPath string, on bool) error {
	p := FlagPath(configPath)
	if on {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return errors.New("cannot create the configuration directory")
		}
		if err := os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
			return errors.New("cannot write the diagnostics flag")
		}
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("cannot remove the diagnostics flag")
	}
	return nil
}

// LogDir is where the agent, app and diagnostics logs live:
// ~/Library/Logs/Nexal on macOS, ~/.local/state/nexal/logs elsewhere.
func LogDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(h, "Library", "Logs", "Nexal"), nil
	}
	return filepath.Join(h, ".local", "state", "nexal", "logs"), nil
}

// RotatingFile is an append-only file capped at max bytes. When a write would
// pass the cap the file is renamed to <path>.1 (replacing any older rotation)
// and a fresh file is started. Safe for concurrent use.
type RotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// NewRotatingFile opens nothing until the first write.
func NewRotatingFile(path string, max int64) *RotatingFile {
	return &RotatingFile{path: path, max: max}
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.openLocked(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		_ = r.f.Close()
		r.f = nil
		_ = os.Rename(r.path, r.path+".1")
		if err := r.openLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Close closes the current file; a later Write reopens it.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Controller owns the runtime log level and the diagnostics file.
type Controller struct {
	level    slog.LevelVar
	baseline slog.Level
	on       atomic.Bool
	flag     string
	base     slog.Handler
	dedup    *Dedup
	file     *RotatingFile
	fileH    slog.Handler
}

// New builds a controller whose ordinary records go to w as JSON at baseline
// level (Debug while diagnostic mode is on). logDir "" disables the separate
// diagnostics file.
func New(w io.Writer, baseline slog.Level, configPath, logDir string) *Controller {
	c := &Controller{baseline: baseline, flag: FlagPath(configPath)}
	c.level.Set(baseline)
	c.dedup = NewDedup(w)
	c.base = slog.NewJSONHandler(c.dedup, &slog.HandlerOptions{Level: &c.level})
	if logDir != "" {
		c.file = NewRotatingFile(filepath.Join(logDir, FileName), MaxFileBytes)
		c.fileH = slog.NewJSONHandler(c.file, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	return c
}

// Flush writes any pending "(repeated N times)" note. Call it on shutdown.
func (c *Controller) Flush() {
	if c.dedup != nil {
		c.dedup.Flush()
	}
}

// Logger returns a logger that writes through this controller.
func (c *Controller) Logger() *slog.Logger {
	return slog.New(&fanout{c: c, base: c.base, file: c.fileH})
}

// On reports whether diagnostic mode is currently applied.
func (c *Controller) On() bool { return c.on.Load() }

// Level is the level currently applied to ordinary records.
func (c *Controller) Level() slog.Level { return c.level.Level() }

// Set applies diagnostic mode and reports whether anything changed.
func (c *Controller) Set(on bool) bool {
	if c.on.Swap(on) == on {
		return false
	}
	if on {
		c.level.Set(slog.LevelDebug)
	} else {
		c.level.Set(c.baseline)
		if c.file != nil {
			_ = c.file.Close()
		}
	}
	return true
}

// Sync reads the flag file once and applies it, logging a change.
func (c *Controller) Sync(logger *slog.Logger) {
	_, err := os.Stat(c.flag)
	on := err == nil
	if on == c.On() {
		return
	}
	if on {
		c.Set(true)
		logger.Info("diagnostic mode on", "level", "debug", "diagnosticsLog", FileName)
		return
	}
	logger.Info("diagnostic mode off", "level", c.baseline.String())
	c.Set(false)
}

// Watch applies the flag file now and every interval until ctx ends.
func (c *Controller) Watch(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	c.Sync(logger)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sync(logger)
		}
	}
}

// fanout sends each record to the ordinary handler and, while diagnostic mode
// is on, to the diagnostics file as well.
type fanout struct {
	c    *Controller
	base slog.Handler
	file slog.Handler // nil when there is no diagnostics file
}

func (h *fanout) toFile(ctx context.Context, l slog.Level) bool {
	return h.file != nil && h.c.on.Load() && h.file.Enabled(ctx, l)
}

func (h *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l) || h.toFile(ctx, l)
}

func (h *fanout) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.base.Enabled(ctx, r.Level) {
		err = h.base.Handle(ctx, r.Clone())
	}
	if h.toFile(ctx, r.Level) {
		_ = h.file.Handle(ctx, r.Clone()) // a full disk must not fail the agent's own logging
	}
	return err
}

func (h *fanout) WithAttrs(as []slog.Attr) slog.Handler {
	n := &fanout{c: h.c, base: h.base.WithAttrs(as)}
	if h.file != nil {
		n.file = h.file.WithAttrs(as)
	}
	return n
}

func (h *fanout) WithGroup(name string) slog.Handler {
	n := &fanout{c: h.c, base: h.base.WithGroup(name)}
	if h.file != nil {
		n.file = h.file.WithGroup(name)
	}
	return n
}
