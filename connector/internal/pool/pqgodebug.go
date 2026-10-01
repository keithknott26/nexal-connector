package pool

import (
	"os"
	"strings"
	"sync"
)

// The peer transport requires SecP384r1MLKEM1024 (NIST level 5). From Go 1.26.x,
// crypto/tls intersects an explicit CurvePreferences list with the GODEBUG-gated
// defaults, so GODEBUG=tlsmlkem=0 or tlssecpmlkem=0 (from the environment, a
// launchd plist, or a parent process) removes the only allowed group. The
// handshake then fails closed ("no supported elliptic curves") rather than
// downgrading, but peers silently stop talking.
//
// enforcePostQuantumGODEBUG strips those two settings from this process's
// GODEBUG before any peer TLS config is built. os.Setenv("GODEBUG", …) is picked
// up live by the runtime (runtime.syscall_runtimeSetenv → godebugNotify), so the
// stdlib sees the default (enabled) value immediately. Other GODEBUG settings are
// preserved. It never weakens anything: it only removes switches that disable
// post-quantum groups.
var pqGODEBUGMu sync.Mutex

// pqGODEBUGKeys are the settings that remove hybrid ML-KEM groups from crypto/tls.
var pqGODEBUGKeys = map[string]bool{"tlsmlkem": true, "tlssecpmlkem": true}

// StrippedPostQuantumGODEBUG returns value without settings that disable the
// hybrid ML-KEM groups, and whether anything was removed.
func StrippedPostQuantumGODEBUG(value string) (string, bool) {
	if value == "" {
		return value, false
	}
	parts := strings.Split(value, ",")
	kept := parts[:0]
	removed := false
	for _, p := range parts {
		key, val, _ := strings.Cut(strings.TrimSpace(p), "=")
		if pqGODEBUGKeys[key] && val != "1" {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ","), removed
}

// EnforcePostQuantumGODEBUG removes PQ-disabling GODEBUG settings from this
// process. It returns true when it changed something, so callers can log it.
func EnforcePostQuantumGODEBUG() bool {
	pqGODEBUGMu.Lock()
	defer pqGODEBUGMu.Unlock()
	current, ok := os.LookupEnv("GODEBUG")
	if !ok {
		return false
	}
	next, removed := StrippedPostQuantumGODEBUG(current)
	if !removed {
		return false
	}
	if next == "" {
		_ = os.Unsetenv("GODEBUG")
	} else {
		_ = os.Setenv("GODEBUG", next)
	}
	return true
}
