package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"nexal/connector/internal/lanshare"
)

// `nexal lan-share` makes this Mac a Time Machine destination for other Macs on
// the local network, using the SMB server macOS already ships.
//
// THIS DELIBERATELY DEVIATES FROM HARDENING-PLAN §21, which specified bundling
// Samba with vfs_fruit. The founder approved the deviation after four findings:
// macOS already supports shared Time Machine destinations on APFS over SMB;
// /usr/sbin/smbd is Apple's smbx rather than Samba and does not read a Samba
// smb.conf; Apple's SMB server already owns port 445 whenever File Sharing is
// on, so a second server cannot bind it; and shipping Samba in a commercial
// product takes on the GPLv3 obligations Apple itself avoided. The scratch-space
// argument for Samba plus JuiceFS does not survive either, because the plan
// already requires tens of GB of local scratch regardless.
//
// `nexal share` is a DIFFERENT COMMAND and still exists: it is the time-boxed
// recoveryOS rescue mode from §9, run on a friend's Mac for one restore. This
// command is the everyday LAN destination on a Mac you own. They are not
// alternatives.
//
// PRIVILEGE, which is the whole design of this command: inspection is entirely
// unprivileged and always runs first. A system that already matches asks for
// nothing at all. Only the specific unmet changes are elevated, one command at a
// time, through macOS's own authorization dialog -- the system draws it, the
// password never passes through this process, and nothing is stored. There is no
// setuid helper and no sudo invocation.
//
// NOT POST-QUANTUM, and the output says so every time. LAN SMB3 is AES-GCM with
// a classical key agreement. The post-quantum claim in this product belongs to
// `nexal drive` (ML-KEM-768) and to escrow, not to a Time Machine share.
func lanShareCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal lan-share status|setup|destination [flags] [--config absolute-path]")
	}
	switch args[0] {
	case "status":
		return lanShareStatus(ctx, args[1:])
	case "setup":
		return lanShareSetup(ctx, args[1:])
	case "destination":
		return lanShareDestination(args[1:])
	default:
		return fmt.Errorf("unknown lan-share subcommand %q", args[0])
	}
}

const transportNote = "LAN SMB3 is AES-GCM with a classical key agreement; it is NOT post-quantum. `nexal drive` is."

// lanShareDesired reads the two flags every subcommand needs.
func lanShareDesired(f interface {
	String(string, string, string) *string
}) (*string, *string) {
	path := f.String("path", "", "absolute path of the folder to publish (must be on an APFS volume)")
	name := f.String("name", "", "SMB share name other Macs will see")
	return path, name
}

func lanShareStatus(ctx context.Context, args []string) error {
	f, configPath, err := flags("lan-share status")
	if err != nil {
		return err
	}
	path, name := lanShareDesired(f)
	if err := parse(f, args, configPath); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal lan-share status --path /abs/folder --name ShareName")
	}
	if err := lanshare.Available(); err != nil {
		return err
	}
	desired := lanshare.Desired{Path: *path, Name: *name}
	if err := desired.Validate(); err != nil {
		return err
	}
	state, err := lanshare.Inspect(ctx, lanshare.ExecRunner{}, desired)
	if err != nil {
		return err
	}
	plan, err := lanshare.BuildPlan(desired, state)
	if err != nil {
		return err
	}
	pending := make([]map[string]any, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		pending = append(pending, map[string]any{
			"step":      s.Name,
			"reason":    s.Reason,
			"needsRoot": s.NeedsRoot,
			"command":   s.String(),
		})
	}
	return emit(map[string]any{
		"path":               desired.Path,
		"name":               desired.Name,
		"configured":         state.Satisfied(desired),
		"fileSharingEnabled": state.FileSharingEnabled,
		"shareExists":        state.ShareExists,
		"sharePath":          state.SharePath,
		"timeMachineEnabled": state.TimeMachineEnabled,
		"filesystem":         state.Filesystem,
		"apfs":               state.APFS(),
		"pending":            pending,
		"needsRoot":          plan.NeedsRoot(),
		"blocked":            plan.Blocked,
		"transport":          transportNote,
	})
}

func lanShareSetup(ctx context.Context, args []string) error {
	f, configPath, err := flags("lan-share setup")
	if err != nil {
		return err
	}
	path, name := lanShareDesired(f)
	// Default true: the point of this command is to finish the job. An owner who
	// wants the commands instead of the dialog passes --elevate=false, which is
	// the documented escape hatch for anyone who would rather read a command
	// before running it as root.
	elevate := f.Bool("elevate", true, "prompt with macOS's authorization dialog for the steps that need root")
	dryRun := f.Bool("dry-run", false, "print the steps that would run and change nothing")
	if err := parse(f, args, configPath); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal lan-share setup --path /abs/folder --name ShareName [--dry-run] [--elevate=false]")
	}
	if err := lanshare.Available(); err != nil {
		return err
	}
	desired := lanshare.Desired{Path: *path, Name: *name}
	if err := desired.Validate(); err != nil {
		return err
	}
	runner := lanshare.ExecRunner{}
	state, err := lanshare.Inspect(ctx, runner, desired)
	if err != nil {
		return err
	}
	plan, err := lanshare.BuildPlan(desired, state)
	if err != nil {
		return err
	}
	if len(plan.Blocked) > 0 {
		return fmt.Errorf("cannot configure the share: %s", plan.Blocked[0])
	}
	if len(plan.Steps) == 0 {
		return emit(map[string]any{
			"path": desired.Path, "name": desired.Name,
			"configured": true, "changed": []string{},
			"note":      "already configured; nothing to do and no administrator rights needed",
			"transport": transportNote,
		})
	}

	commands := make([]string, 0, len(plan.Steps))
	reasons := make([]map[string]any, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		commands = append(commands, s.String())
		reasons = append(reasons, map[string]any{"step": s.Name, "reason": s.Reason, "needsRoot": s.NeedsRoot})
	}
	if *dryRun {
		return emit(map[string]any{
			"path": desired.Path, "name": desired.Name,
			"dryRun": true, "wouldRun": reasons, "commands": commands,
			"needsRoot": plan.NeedsRoot(), "transport": transportNote,
		})
	}

	// Say what the password is for BEFORE the dialog appears, on stderr so it is
	// visible even when stdout is being parsed. A tool that raises an
	// authorization prompt with no preceding explanation teaches people to approve
	// prompts they have not read.
	if plan.NeedsRoot() && os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "These changes need administrator rights:\n")
		for _, s := range plan.Steps {
			if s.NeedsRoot {
				fmt.Fprintf(os.Stderr, "  - %s: %s\n    %s\n", s.Name, s.Reason, s.String())
			}
		}
		if *elevate {
			fmt.Fprintf(os.Stderr, "macOS will ask for your password once per step. It is not seen or stored by nexal.\n")
		}
	}

	var elevator lanshare.Elevator
	if *elevate {
		elevator = lanshare.NewOsascriptElevator(runner)
	}
	changed, applyErr := lanshare.Apply(ctx, plan, runner, elevator)
	if applyErr != nil {
		// Report what DID happen before failing: a partially applied plan is the
		// state the Mac is actually in, and hiding it would leave the owner
		// guessing which steps to repeat.
		if len(changed) > 0 {
			fmt.Fprintf(os.Stderr, "applied before failing: %v\n", changed)
		}
		return applyErr
	}

	// Verify rather than trust. A command exiting zero is not evidence the share
	// is usable, and this is the last moment anyone is paying attention.
	after, err := lanshare.Inspect(ctx, runner, desired)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	destination, _ := lanshare.DestinationURL(hostLocal(host), currentUsername(), desired.Name)
	return emit(map[string]any{
		"path": desired.Path, "name": desired.Name,
		"changed":    changed,
		"configured": after.Satisfied(desired),
		"verified": map[string]any{
			"fileSharingEnabled": after.FileSharingEnabled,
			"shareExists":        after.ShareExists,
			"timeMachineEnabled": after.TimeMachineEnabled,
			"apfs":               after.APFS(),
		},
		"destination": destination,
		"next":        "on the other Mac: sudo tmutil setdestination -a " + destination,
		"transport":   transportNote,
	})
}

func lanShareDestination(args []string) error {
	f, configPath, err := flags("lan-share destination")
	if err != nil {
		return err
	}
	name := f.String("name", "", "SMB share name")
	host := f.String("host", "", "host name of this Mac as other Macs see it (default: this Mac's hostname)")
	user := f.String("user", "", "account other Macs will authenticate as (default: the current user)")
	if err := parse(f, args, configPath); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal lan-share destination --name ShareName [--host name] [--user account]")
	}
	h := *host
	if h == "" {
		n, err := os.Hostname()
		if err != nil {
			return errors.New("could not determine this Mac's hostname; pass --host")
		}
		h = hostLocal(n)
	}
	u := *user
	if u == "" {
		u = currentUsername()
	}
	url, err := lanshare.DestinationURL(h, u, *name)
	if err != nil {
		return err
	}
	return emit(map[string]any{
		"destination": url,
		"command":     "sudo tmutil setdestination -a " + url,
		// No password in the URL, deliberately: tmutil accepts one and using it
		// would put the share credential into that Mac's shell history and process
		// list. tmutil prompts instead.
		"note":      "tmutil will prompt for the share password; do not put it in the URL",
		"transport": transportNote,
	})
}

// hostLocal returns a name other Macs can resolve. A bare short hostname is not
// resolvable on a LAN; the mDNS name is.
func hostLocal(name string) string {
	if name == "" {
		return ""
	}
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			return name // already qualified, including an existing .local
		}
	}
	return name + ".local"
}

func currentUsername() string {
	for _, k := range []string{"USER", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
