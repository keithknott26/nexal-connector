// Package lanshare turns this Mac into a Time Machine destination for other
// Macs on the local network using the SMB server macOS already ships.
//
// # Why this replaces the hardening plan's bundled-Samba design
//
// The plan (§21) called for shipping Samba with vfs_fruit and supervising it the
// way cloudflared is supervised. On macOS that is the wrong build, for four
// independent reasons, and the founder accepted the deviation:
//
//  1. macOS already supports exactly this. Apple documents sharing a folder on
//     an APFS volume as a Time Machine destination for other Macs over SMB, so
//     the capability exists in the OS and needs configuring, not replacing.
//  2. /usr/sbin/smbd on macOS is Apple's own smbx, not Samba. Apple replaced
//     Samba at 10.7 specifically to avoid GPLv3. A pinned binary named smbd
//     would find a program that does not read a Samba smb.conf at all.
//  3. Port 445 is already Apple's whenever File Sharing is on. A second SMB
//     server cannot bind it, so a bundled Samba breaks the moment the user
//     enables ordinary file sharing.
//  4. Shipping Samba in a commercial product takes on the GPLv3 obligations
//     Apple itself walked away from.
//
// The scratch-space argument that was supposed to justify Samba plus JuiceFS
// does not survive either: the plan already concedes tens of GB of local scratch
// are required regardless, and once the bytes are local, the folder holding them
// can be shared natively.
//
// # Privilege
//
// This package never escalates on its own. Inspection is entirely unprivileged.
// Changes are returned as a Plan of explicit argv steps, each marked with
// whether it needs root, and a step that needs root is executed only when the
// process already has it. Otherwise the exact command is reported for the owner
// to run deliberately. There is no setuid helper, no sudo invocation, and no
// stored admin password.
//
// # What is NOT verified
//
// No macOS machine or `sharing`/`tmutil`/`diskutil` binary exists in the
// environment this package was written in. Command construction, parsing,
// planning and refusals are covered by tests against recorded output and fake
// runners; the real system commands have never been executed. Treat the first
// run on a Mac as the acceptance test.
package lanshare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner executes a command and returns its combined output. It exists so the
// planner and the parsers can be tested without a Mac, and so that every
// external command in this package goes through one auditable chokepoint.
//
// Implementations MUST pass argv directly to exec and never through a shell.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

// Paths of the system tools used. Absolute, because resolving `sharing` or
// `tmutil` through PATH would let a directory earlier in PATH substitute a
// different program for one we are about to run with root.
const (
	SharingPath   = "/usr/sbin/sharing"
	TmutilPath    = "/usr/bin/tmutil"
	DiskutilPath  = "/usr/sbin/diskutil"
	LaunchctlPath = "/bin/launchctl"

	// smbdService is the launchd label for Apple's SMB server. Enabling File
	// Sharing in System Settings is what loads it.
	smbdService = "system/com.apple.smbd"
)

// MaxShareNameLength bounds the SMB share name. SMB itself allows more, but a
// name this long is already a mistake and the bound keeps the value printable
// in every diagnostic.
const MaxShareNameLength = 64

// ValidateShareName refuses anything that is not a plain, safely quotable SMB
// share name.
//
// This is strict on purpose. The name reaches argv for a command that may run as
// root, it reaches a URL in `tmutil setdestination`, and it is displayed to the
// person restoring a machine. A name that means one thing in one of those places
// and something else in another is how a share ends up pointing somewhere
// unintended.
func ValidateShareName(name string) error {
	if name == "" {
		return errors.New("lanshare: share name must not be empty")
	}
	if len(name) > MaxShareNameLength {
		return fmt.Errorf("lanshare: share name must be at most %d characters", MaxShareNameLength)
	}
	// Leading '-' would be read as a flag by any command taking the name.
	if strings.HasPrefix(name, "-") {
		return errors.New("lanshare: share name must not begin with '-'")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("lanshare: share name may contain only letters, digits, '-', '_' and '.'; %q is not allowed", r)
		}
	}
	if name == "." || name == ".." {
		return errors.New("lanshare: share name must not be '.' or '..'")
	}
	return nil
}

// ValidateSharePath refuses a path that cannot safely be shared.
//
// The checks that matter: the path must be absolute and already exist as a real
// directory, and must not be a symlink. Sharing a symlink would publish
// whatever it points at now and whatever it is repointed at later, which turns a
// backup share into an arbitrary-file-read of this Mac.
func ValidateSharePath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("lanshare: share path must be absolute")
	}
	if path != filepath.Clean(path) {
		return fmt.Errorf("lanshare: share path must be in canonical form (%s)", filepath.Clean(path))
	}
	if path == "/" {
		return errors.New("lanshare: refusing to share the root of the filesystem")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("lanshare: %s does not exist; create the folder first", path)
		}
		return fmt.Errorf("lanshare: cannot inspect %s", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lanshare: %s is a symbolic link; share the real directory so the destination cannot be repointed later", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("lanshare: %s is not a directory", path)
	}
	// Refuse a location whose contents are obviously not ours to publish. These
	// are the mistakes that would expose an entire home directory or system
	// volume over SMB with one typo.
	for _, forbidden := range []string{"/System", "/Library", "/bin", "/sbin", "/usr", "/etc", "/var", "/private", "/Applications"} {
		if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
			return fmt.Errorf("lanshare: refusing to share %s; choose a dedicated backup folder", path)
		}
	}
	if path == os.Getenv("HOME") {
		return errors.New("lanshare: refusing to share the whole home directory; choose a dedicated backup folder")
	}
	return nil
}

// Desired is the configuration the owner asked for.
type Desired struct {
	// Path is the directory to publish. Must be on an APFS volume, because Apple
	// requires APFS for a shared Time Machine destination.
	Path string
	// Name is the SMB share name other Macs will see.
	Name string
}

// Validate checks a Desired before any command runs.
func (d Desired) Validate() error {
	if err := ValidateShareName(d.Name); err != nil {
		return err
	}
	return ValidateSharePath(d.Path)
}

// State is what inspection found. Every field is a fact read from the system,
// never an assumption: a field that could not be determined is reported through
// Unknown rather than defaulted, because defaulting "is File Sharing on?" to
// false would produce a plan that tries to enable something already running.
type State struct {
	// FileSharingEnabled is whether Apple's SMB server is loaded in launchd.
	FileSharingEnabled bool
	// ShareExists is whether a share point with the desired name is present.
	ShareExists bool
	// SharePath is the path the existing share publishes, when ShareExists.
	SharePath string
	// TimeMachineEnabled is whether the existing share is flagged as a Time
	// Machine destination.
	TimeMachineEnabled bool
	// Filesystem is the volume format of the desired path, e.g. "apfs".
	Filesystem string
	// Unknown lists facts that could not be established, with the reason.
	Unknown map[string]string
}

// APFS reports whether the inspected path is on APFS. Apple requires it for a
// shared Time Machine destination, so a false here is a hard stop rather than a
// warning.
func (s State) APFS() bool { return strings.EqualFold(s.Filesystem, "apfs") }

// Satisfied reports whether the system already matches what was asked for.
func (s State) Satisfied(d Desired) bool {
	return s.FileSharingEnabled && s.ShareExists && s.TimeMachineEnabled &&
		s.SharePath == d.Path && s.APFS()
}
