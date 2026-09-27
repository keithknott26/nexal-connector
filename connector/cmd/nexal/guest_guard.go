package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
)

const guestGuardLabel = "systems.nexal.guest-expiry"
const guestGuardDirectory = "/Library/PrivilegedHelperTools/neXalGuest"
const guestGuardExecutable = guestGuardDirectory + "/nexal"
const guestGuardPlist = "/Library/LaunchDaemons/" + guestGuardLabel + ".plist"

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func guestGuardDefinition(executable, path string, g config.GuestAccess) []byte {
	args := []string{executable, "guest", "guard", "--config", path, "--grant-id", g.GrantID, "--deadline", g.AccessExpiresAt, "--received-at", g.ReceivedAt, "--contact", g.InviterEmail}
	text := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + guestGuardLabel + `</string><key>ProgramArguments</key><array>`
	for _, arg := range args {
		text += "<string>" + xmlText(arg) + "</string>"
	}
	text += `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>30</integer><key>ProcessType</key><string>Background</string><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`
	return []byte(text)
}
func installGuestGuard(ctx context.Context, path string) error {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		return errors.New("temporary-access expiry guard requires macOS administrator installation")
	}
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	if c.GuestAccess == nil || c.GuestAccess.IsExpired(time.Now()) {
		return errors.New("temporary access has expired; request a new code")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	st, err := os.Lstat(exe)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe expiry guard executable")
	}
	if err = installRootGuestHelpers(exe); err != nil {
		return err
	}
	definition := guestGuardDefinition(guestGuardExecutable, path, *c.GuestAccess)
	dir, err := os.OpenRoot("/Library/LaunchDaemons")
	if err != nil {
		return err
	}
	defer dir.Close()
	filename := guestGuardLabel + ".plist"
	temp := filename + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	f, err := dir.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer dir.Remove(temp)
	_, err = f.Write(definition)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = dir.Rename(temp, filename); err != nil {
		return err
	}
	request, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	_ = exec.CommandContext(request, "/bin/launchctl", "bootout", "system/"+guestGuardLabel).Run()
	if err = exec.CommandContext(request, "/bin/launchctl", "bootstrap", "system", guestGuardPlist).Run(); err != nil {
		return errors.New("expiry guard could not start; this Mac has not joined")
	}
	return nil
}
func guestGuardReady(ctx context.Context, path string, g config.GuestAccess) bool {
	if !rootOwnedImmutablePath(guestGuardExecutable) || !rootOwnedImmutablePath(guestGuardDirectory+"/nexal-network") {
		return false
	}
	st, err := os.Lstat(guestGuardPlist)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return false
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return false
	}
	raw, err := os.ReadFile(guestGuardPlist)
	if err != nil || !bytes.Equal(raw, guestGuardDefinition(guestGuardExecutable, path, g)) {
		return false
	}
	request, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	out, err := exec.CommandContext(request, "/bin/launchctl", "print", "system/"+guestGuardLabel).Output()
	return err == nil && strings.Contains(string(out), "state = running")
}

// The root launchd job survives closing the menu app and starts at reboot. Its
// pinned deadline is in the root-owned launchd definition, not merely UI state.
// It never fetches network state, executes shell text, or writes user Keychain.
func runGuestGuard(ctx context.Context, path string, grant config.GuestAccess, runner mesh.CommandRunner) error {
	if grant.Validate() != nil {
		return errors.New("invalid expiry guard lease")
	}
	initial := time.Now()
	deadline, _ := time.Parse(time.RFC3339Nano, grant.AccessExpiresAt)
	maximum := time.Until(deadline)
	if maximum < 0 {
		maximum = 0
	}
	timer := time.NewTimer(maximum)
	defer timer.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	expired := grant.IsExpired(initial)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Do not read the user's mutable config to relax a root-pinned lease.
		// Only a new administrator-installed grant or explicit removal replaces it.
		if grant.IsExpired(time.Now()) {
			expired = true
		}
		if expired {
			request, stop := context.WithTimeout(ctx, 8*time.Second)
			downErr := runner.Run(request, "nexal-network", "down")
			stop()
			// Keep retrying a failed local disconnect, including daemon restarts.
			if downErr != nil {
				// If its control socket is stuck, stop the fixed networking
				// service as root rather than treating a failed down as expiry.
				fallback, finish := context.WithTimeout(ctx, 8*time.Second)
				_ = runner.Run(fallback, "nexal-network", "service", "stop")
				finish()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			expired = true
		case <-ticker.C:
		}
	}
}
func guestExpiredError(g *config.GuestAccess) error {
	if g != nil {
		return fmt.Errorf("temporary network access expired; ask %s for a new invitation code", g.InviterEmail)
	}
	return errors.New("temporary network access expired; request a new invitation code")
}

func removeGuestGuard(ctx context.Context) error {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		return errors.New("expiry guard removal requires administrator authorization")
	}
	if _, err := os.Lstat(guestGuardPlist); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	request, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	if err := (mesh.ExecRunner{}).Run(request, "nexal-network", "down"); err != nil {
		return errors.New("disconnect temporary access before removing its expiry guard")
	}
	if err := exec.CommandContext(request, "/bin/launchctl", "bootout", "system/"+guestGuardLabel).Run(); err != nil {
		if exec.CommandContext(request, "/bin/launchctl", "print", "system/"+guestGuardLabel).Run() == nil || request.Err() != nil {
			return errors.New("temporary expiry guard is still active; retry administrator removal")
		}
	}
	if err := os.Remove(guestGuardPlist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not remove temporary expiry guard")
	}
	return nil
}

// A user-writable app bundle must never be the executable of a root launchd
// service. Copy only these fixed helpers into a root-owned directory first.
func rootOwnedImmutablePath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return false
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 {
			return false
		}
	}
	return true
}
func installRootGuestHelpers(sourceExecutable string) error {
	if os.Geteuid() != 0 {
		return errors.New("root helper installation requires administrator authorization")
	}
	parent := "/Library/PrivilegedHelperTools"
	if !rootOwnedImmutablePath("/Library") {
		return errors.New("unsafe system library directory")
	}
	if err := os.Mkdir(parent, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !rootOwnedImmutablePath(parent) {
		return errors.New("unsafe privileged helper directory")
	}
	if err := os.Mkdir(guestGuardDirectory, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !rootOwnedImmutablePath(guestGuardDirectory) {
		return errors.New("unsafe guest helper directory")
	}
	root, err := os.OpenRoot(guestGuardDirectory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"nexal", "nexal-network"} {
		source := filepath.Join(filepath.Dir(sourceExecutable), name)
		before, err := os.Lstat(source)
		if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() > 512<<20 {
			return errors.New("unsafe bundled guest helper")
		}
		input, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		actual, e := input.Stat()
		if e != nil || !os.SameFile(before, actual) || !actual.Mode().IsRegular() {
			input.Close()
			return errors.New("bundled guest helper changed")
		}
		temporary := name + "." + strconv.Itoa(os.Getpid()) + ".tmp"
		output, e := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if e != nil {
			input.Close()
			return e
		}
		n, e := io.Copy(output, io.LimitReader(input, (512<<20)+1))
		input.Close()
		if e == nil && n != actual.Size() {
			e = errors.New("bundled guest helper changed size")
		}
		if e == nil {
			e = output.Sync()
		}
		closeErr := output.Close()
		if e != nil || closeErr != nil {
			root.Remove(temporary)
			return errors.New("could not copy privileged guest helper")
		}
		if e = root.Rename(temporary, name); e != nil {
			root.Remove(temporary)
			return e
		}
	}
	return nil
}
