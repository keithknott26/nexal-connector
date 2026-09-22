package lanshare

import (
	"context"
	"fmt"
	"strings"
)

// Inspect reads the current state. Every command here is read-only and none
// needs root, which is what makes "elevate only when needed" possible: the
// decision to prompt is taken after knowing what is already true.
//
// A command that fails does not fail the inspection. Its fact is recorded in
// Unknown instead, and BuildPlan treats an unknown fact as blocking. The
// alternative -- assuming "not configured" when `sharing -l` could not be run --
// would produce a plan that asks for a password to create a share that already
// exists.
func Inspect(ctx context.Context, runner Runner, d Desired) (State, error) {
	if err := d.Validate(); err != nil {
		return State{}, err
	}
	s := State{Unknown: map[string]string{}}

	if out, err := runner.Run(ctx, LaunchctlPath, "print", smbdService); err != nil {
		// launchctl print exits non-zero for a service that is not loaded, which
		// is a legitimate answer rather than a failure to determine the fact.
		s.FileSharingEnabled = false
	} else {
		s.FileSharingEnabled = strings.Contains(out, "state = running") ||
			strings.Contains(out, "state = waiting")
	}

	out, err := runner.Run(ctx, SharingPath, "-l")
	if err != nil {
		s.Unknown["whether the share already exists"] = "could not list share points"
	} else {
		share, found := parseShareList(out, d.Name)
		s.ShareExists = found
		s.SharePath = share.path
		s.TimeMachineEnabled = share.timeMachine
	}

	fs, err := volumeFormat(ctx, runner, d.Path)
	if err != nil {
		s.Unknown["the volume format of "+d.Path] = err.Error()
	} else {
		s.Filesystem = fs
	}
	return s, nil
}

type shareRecord struct {
	path        string
	timeMachine bool
}

// parseShareList reads `sharing -l` output.
//
// The format is a list of records with "name:" and indented attributes. It is
// not a documented interface, so parsing is deliberately forgiving about layout
// and strict about attribution: a value is only ever assigned to the record
// whose name matched, so an unexpected line cannot leak an attribute from a
// neighbouring share into this one.
func parseShareList(out, want string) (shareRecord, bool) {
	var (
		rec    shareRecord
		inWant bool
		found  bool
	)
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// A record header is an unindented "name: value" whose key is the share
		// name field. Indentation is what distinguishes a header from an
		// attribute, so it is read from the raw line, not the trimmed one.
		if !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				inWant = false
				continue
			}
			if strings.EqualFold(strings.TrimSpace(key), "name") {
				inWant = strings.TrimSpace(value) == want
				if inWant {
					found = true
				}
				continue
			}
			inWant = false
			continue
		}
		if !inWant {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "path":
			rec.path = value
		case "smb time machine", "time machine", "timemachine":
			rec.timeMachine = isTruthy(value)
		}
	}
	return rec, found
}

// isTruthy reads the several spellings this family of tools uses for a boolean.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	}
	return false
}

// volumeFormat returns the filesystem type of the volume containing path.
//
// -plist is not used because parsing plist would add a dependency for one field;
// the human-readable output's "Type (Bundle)" line carries the same value. If
// the field is absent the error says so rather than returning an empty string
// that would read as "not APFS".
func volumeFormat(ctx context.Context, runner Runner, path string) (string, error) {
	out, err := runner.Run(ctx, DiskutilPath, "info", path)
	if err != nil {
		return "", fmt.Errorf("could not read the volume information for %s", path)
	}
	for _, raw := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "type (bundle)", "file system personality":
			v := strings.TrimSpace(value)
			if v == "" {
				continue
			}
			// "APFS" and "Case-sensitive APFS" both mean APFS.
			if strings.Contains(strings.ToLower(v), "apfs") {
				return "apfs", nil
			}
			return strings.ToLower(v), nil
		}
	}
	return "", fmt.Errorf("the volume information for %s did not name a filesystem", path)
}

// DestinationURL is the value another Mac passes to `tmutil setdestination`.
//
// The password is deliberately NOT included. tmutil accepts one in the URL, and
// using it would put the share credential into that Mac's shell history, its
// process list while running, and any shell transcript. tmutil prompts for the
// password when it is absent, which is the same information over a channel that
// does not persist it.
func DestinationURL(host, username, share string) (string, error) {
	if err := ValidateShareName(share); err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("lanshare: no host name for the destination")
	}
	for _, r := range host + username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_', r == '@':
		default:
			return "", fmt.Errorf("lanshare: %q is not valid in a destination URL", r)
		}
	}
	if username == "" {
		return "smb://" + host + "/" + share, nil
	}
	return "smb://" + username + "@" + host + "/" + share, nil
}
