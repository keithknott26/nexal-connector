//go:build darwin || linux

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// machineLockName is deliberately NOT derived from the config path.
//
// Lock() locks "agent.lock" beside the configuration, which stops a second connector on
// the SAME configuration and nothing else. The macOS app keeps a separate development
// profile in "Application Support/Nexal-Development", so a development connector and a
// production connector took two different locks and both ran -- two agents heartbeating,
// advertising addresses and answering as the same host. Any lock that lives next to the
// configuration reproduces that bug by construction, so this one lives at a fixed
// per-user path that every configuration resolves to identically.
const machineLockName = "connector.single.lock"

// machineStateDir is the fixed directory for host-wide connector state, chosen without
// reference to which configuration is in use. It intentionally matches the production
// config directory so an existing install gains no new directory.
func machineStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Nexal"), nil
	}
	return filepath.Join(home, ".config", "nexal"), nil
}

// LockMachine admits one running connector per user on this Mac, whatever configuration
// it was started with. Callers keep holding it for the process lifetime.
//
// configPath is recorded in the lock file rather than used to choose it, so the process
// that loses the race can say which configuration already holds the machine instead of
// only that something does.
func LockMachine(configPath string) (func(), error) {
	dir, err := machineStateDir()
	if err != nil {
		return nil, errors.New("cannot resolve the connector state directory")
	}
	// 0700: the lock file records an absolute configuration path, and the existing
	// Lock() already refuses anything group- or world-accessible.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("cannot create the connector state directory")
	}
	path := filepath.Join(dir, machineLockName)

	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errors.New("cannot open the host connector lock")
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return nil, errors.New("host connector lock must be a private regular file")
	}

	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Read the holder's details BEFORE closing. Failing with "already running" and
		// nothing else is what made the original duplicate hard to explain: both
		// processes looked identical from the outside.
		holder := readHolder(f)
		_ = f.Close()
		if holder != "" {
			return nil, fmt.Errorf("another connector is already running on this Mac (%s); "+
				"only one may run per user, whatever configuration it uses", holder)
		}
		return nil, errors.New("another connector is already running on this Mac; " +
			"only one may run per user, whatever configuration it uses")
	}

	// Recorded only after the lock is held, so the contents always describe the current
	// holder and never a process that failed to start.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d\nconfig %s\n", os.Getpid(), configPath)), 0)
	}

	return func() {
		// Cleared on release so a stale configuration path is never reported against a
		// process that has exited. The file itself stays: recreating it on every start
		// would race another starting connector.
		_ = f.Truncate(0)
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// readHolder extracts a one-line description of the process holding the lock. Best
// effort by design: an empty or unreadable file must degrade to a plain message rather
// than block a legitimate start.
func readHolder(f *os.File) string {
	buf := make([]byte, 512)
	n, _ := f.ReadAt(buf, 0)
	if n <= 0 {
		return ""
	}
	var pid, cfg string
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "pid "):
			pid = strings.TrimSpace(strings.TrimPrefix(line, "pid "))
		case strings.HasPrefix(line, "config "):
			cfg = strings.TrimSpace(strings.TrimPrefix(line, "config "))
		}
	}
	switch {
	case pid != "" && cfg != "":
		return "pid " + pid + ", config " + cfg
	case pid != "":
		return "pid " + pid
	case cfg != "":
		return "config " + cfg
	}
	return ""
}
