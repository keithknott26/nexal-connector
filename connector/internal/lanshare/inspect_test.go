package lanshare

import (
	"context"
	"errors"
	"testing"
)

// Recorded shape of `sharing -l` output. Two records, so attribution can be
// checked: an attribute of one share must never be read as an attribute of the
// other.
const sharingList = `List of Share Points
name:		Backups
	path:		/Users/kknott/Backups
	afp:	{
		name:	Backups
		shared:	0
	}
	smb:	{
		name:	Backups
		shared:	1
		guest access:	0
	}
	smb time machine:	1
name:		Media
	path:		/Volumes/Media
	smb:	{
		name:	Media
		shared:	1
	}
	smb time machine:	0
`

func TestShareListParsesTheRequestedRecordOnly(t *testing.T) {
	got, found := parseShareList(sharingList, "Backups")
	if !found {
		t.Fatal("the Backups share was not found")
	}
	if got.path != "/Users/kknott/Backups" {
		t.Errorf("path = %q", got.path)
	}
	if !got.timeMachine {
		t.Error("the Time Machine flag was not read")
	}

	media, found := parseShareList(sharingList, "Media")
	if !found {
		t.Fatal("the Media share was not found")
	}
	// The crucial case: Media must NOT inherit Backups' Time Machine flag.
	if media.timeMachine {
		t.Error("an attribute leaked from the neighbouring share record")
	}
	if media.path != "/Volumes/Media" {
		t.Errorf("path = %q", media.path)
	}
}

func TestShareListReportsAbsenceRatherThanAnEmptyRecord(t *testing.T) {
	if _, found := parseShareList(sharingList, "Absent"); found {
		t.Error("a share that does not exist was reported as found")
	}
	if _, found := parseShareList("", "Backups"); found {
		t.Error("empty output produced a share")
	}
	// A name must match exactly: a prefix is a different share.
	if _, found := parseShareList(sharingList, "Backup"); found {
		t.Error("a prefix matched a different share name")
	}
}

func TestTruthyAcceptsTheSpellingsTheseToolsUse(t *testing.T) {
	for _, yes := range []string{"1", "true", "TRUE", "yes", "On", "enabled"} {
		if !isTruthy(yes) {
			t.Errorf("%q read as false", yes)
		}
	}
	for _, no := range []string{"0", "false", "no", "off", "", "maybe"} {
		if isTruthy(no) {
			t.Errorf("%q read as true", no)
		}
	}
}

const diskutilAPFS = `   Device Identifier:         disk3s5
   Volume Name:               Macintosh HD
   File System Personality:   APFS
   Type (Bundle):             apfs
   Mounted:                   Yes
`

const diskutilHFS = `   Device Identifier:         disk4s2
   Volume Name:               Old Backup
   File System Personality:   Journaled HFS+
   Type (Bundle):             hfs
`

func TestVolumeFormatReadsAPFSAndNonAPFS(t *testing.T) {
	ctx := context.Background()
	apfs := &fakeRunner{out: map[string]string{DiskutilPath: diskutilAPFS}}
	if fs, err := volumeFormat(ctx, apfs, "/Users/kknott/Backups"); err != nil || fs != "apfs" {
		t.Fatalf("APFS volume read as %q (err %v)", fs, err)
	}
	hfs := &fakeRunner{out: map[string]string{DiskutilPath: diskutilHFS}}
	fs, err := volumeFormat(ctx, hfs, "/Volumes/Old")
	if err != nil {
		t.Fatalf("volumeFormat: %v", err)
	}
	if fs == "apfs" {
		t.Fatal("an HFS+ volume was read as APFS")
	}
}

// A missing filesystem field must error, not return "" -- an empty string would
// read downstream as "not APFS" and block for the wrong reason, or worse be
// treated as unset and skip the check entirely.
func TestVolumeFormatErrorsWhenTheFieldIsAbsent(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{DiskutilPath: "Device Identifier: disk1\nMounted: Yes\n"}}
	if _, err := volumeFormat(context.Background(), runner, "/x"); err == nil {
		t.Fatal("absent filesystem information did not error")
	}
	failing := &fakeRunner{fail: map[string]error{DiskutilPath: errors.New("exit 1")}}
	if _, err := volumeFormat(context.Background(), failing, "/x"); err == nil {
		t.Fatal("a failing diskutil did not error")
	}
}

// A failed `sharing -l` must become an unknown fact, not a false.
func TestInspectRecordsUnknownFactsInsteadOfAssuming(t *testing.T) {
	dir := shareDir(t)
	runner := &fakeRunner{
		out:  map[string]string{DiskutilPath: diskutilAPFS},
		fail: map[string]error{SharingPath: errors.New("exit 1")},
	}
	state, err := Inspect(context.Background(), runner, Desired{Path: dir, Name: "Backups"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(state.Unknown) == 0 {
		t.Fatal("a failed share listing did not record an unknown fact")
	}
	if state.ShareExists {
		t.Error("an unknown share was reported as existing")
	}
}

func TestInspectUsesOnlyReadOnlyCommands(t *testing.T) {
	dir := shareDir(t)
	runner := &fakeRunner{out: map[string]string{
		DiskutilPath:  diskutilAPFS,
		SharingPath:   sharingList,
		LaunchctlPath: "state = running",
	}}
	state, err := Inspect(context.Background(), runner, Desired{Path: dir, Name: "Backups"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !state.FileSharingEnabled {
		t.Error("a running smbd was not detected")
	}
	if !state.APFS() {
		t.Error("APFS was not detected")
	}
	// Nothing in inspection may mutate the system.
	for _, call := range runner.calls {
		for _, arg := range call[1:] {
			switch arg {
			case "-a", "-e", "-r", "enable", "disable", "bootstrap", "setdestination":
				t.Errorf("inspection ran a mutating command: %v", call)
			}
		}
	}
	if len(runner.calls) != 3 {
		t.Errorf("expected three read-only calls, got %v", runner.calls)
	}
}
