package cybersecurity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testScanner(t *testing.T) (Scanner, string) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	files := filepath.Join(base, "files")
	for _, p := range []string{state, files} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// macOS temporary paths may have /var alias; use canonical roots in tests.
	state, _ = filepath.EvalSymlinks(state)
	files, _ = filepath.EvalSymlinks(files)
	return Scanner{state}, files
}
func testEngine(t *testing.T) string {
	t.Helper()
	p := os.Getenv("NEXAL_TEST_YARAX")
	if p == "" {
		t.Skip("set NEXAL_TEST_YARAX to the pinned yr1.20.0 binary for actual-engine integration")
	}
	return p
}
func TestScannerActualYARAAndDurableOutbox(t *testing.T) {
	engine := testEngine(t)
	s, files := testScanner(t)
	ctx := context.Background()
	now := time.Now()
	// Harmless industry standard test pattern; never execute it.
	eicar := `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
	if err := os.WriteFile(filepath.Join(files, "private-filename.txt"), []byte(eicar), 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(files, "benign.txt"), []byte("hello world"), 0600)
	if err := s.Configure(true, []string{files}, engine); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var first Event
	offline := func(_ context.Context, event Event) error { calls++; first = event; return errors.New("offline") }
	if err := s.Scan(ctx, now, offline, true); err != nil {
		t.Fatal(err)
	}
	state, err := s.Status()
	if err != nil || state.PendingEvents != 1 || state.FilesScanned != 2 || state.EngineVersion != EngineVersion {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if calls != 1 || first.Evidence == nil || !first.Evidence.TestOnly || first.Severity != "info" {
		t.Fatalf("actual yr did not emit test-only EICAR: %+v", first)
	}
	ledger, err := s.Findings()
	if err != nil || len(ledger) != 1 || ledger[0].Path != filepath.Join(files, "private-filename.txt") {
		t.Fatalf("missing local finding: %+v %v", ledger, err)
	}
	statusJSON, _ := json.Marshal(state)
	if strings.Contains(string(statusJSON), "private-filename") {
		t.Fatal("local ledger escaped into status")
	}
	raw, _ := json.Marshal(first)
	if output := os.Getenv("NEXAL_TEST_EVENT_OUTPUT"); output != "" {
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, secret := range []string{eicar, files, "private-filename"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private content escaped event")
		}
	}
	if err := os.Rename(filepath.Join(files, "benign.txt"), filepath.Join(files, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	// New object mimics process restart; content cache also survives a rename.
	again := Scanner{s.Directory}
	delivered := 0
	if err := again.Scan(ctx, now.Add(time.Minute), func(_ context.Context, event Event) error {
		delivered++
		if event.EventID != first.EventID {
			t.Fatal("retry identity changed")
		}
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	state, _ = again.Status()
	if delivered != 1 || state.PendingEvents != 0 || state.FilesScanned != 0 {
		t.Fatalf("unchanged files rescanned or delivery failed: %+v", state)
	}
	if err := again.Scan(ctx, now.Add(2*time.Minute), nil, false); err != nil {
		t.Fatal(err)
	}
	later, _ := again.Status()
	if later.LastAttemptAt != state.LastAttemptAt {
		t.Fatal("scheduled scan ignored fifteen-minute cadence")
	}
}
func TestScannerTraversalBoundsAndSymlinks(t *testing.T) {
	s, files := testScanner(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(files, "link"))
	large, err := os.Create(filepath.Join(files, "large"))
	if err != nil {
		t.Fatal(err)
	}
	large.Truncate(MaxScanFileBytes + 1)
	large.Close()
	os.WriteFile(filepath.Join(files, "safe"), []byte("safe"), 0600)
	visited := 0
	skipped, err := walkInputs(context.Background(), []string{files}, func(_ string, b []byte) error {
		visited++
		if string(b) != "safe" {
			t.Fatal("read outside allowed bound")
		}
		return nil
	})
	if err != nil || visited != 1 || skipped != 2 {
		t.Fatalf("visited=%d skipped=%d error=%v", visited, skipped, err)
	}
	if s.Configure(true, []string{"/"}, "/x") == nil {
		t.Fatal("whole filesystem root accepted")
	}
	alias := filepath.Join(filepath.Dir(files), "alias")
	os.Symlink(files, alias)
	if s.Configure(true, []string{alias}, "/x") == nil {
		t.Fatal("symlink root accepted")
	}
	if s.Configure(true, []string{s.Directory}, "/x") == nil {
		t.Fatal("private state scan accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = walkInputs(ctx, []string{files}, func(string, []byte) error { return nil }); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestBaselineRequiresExplicitApprovalAndNeverRetrains(t *testing.T) {
	s, files := testScanner(t)
	original := strings.Repeat("# a documented line\n", 30)
	for _, name := range []string{"a.py", "b.py", "c.py"} {
		os.WriteFile(filepath.Join(files, name), []byte(original), 0600)
	}
	if err := s.Configure(true, []string{files}, "/missing-engine"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Status()
	if before.BaselineID != "" {
		t.Fatal("configuration implicitly approved baseline")
	}
	if err := s.ApproveBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	approved, _ := s.Status()
	if !digestID.MatchString(approved.BaselineID) {
		t.Fatal("baseline not persisted")
	}
	os.WriteFile(filepath.Join(files, "a.py"), []byte(strings.Repeat("\t\t\t\tVERY_LONG_VARIABLE_NAME = 12345678901234567890123456789012345678901234567890\n", 30)), 0600)
	if err := s.Scan(context.Background(), time.Now(), nil, true); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Status()
	if after.BaselineID != approved.BaselineID || after.Status != "engine_unavailable" {
		t.Fatal("scan changed trusted baseline or hid unavailable engine")
	}
}
func TestStyleDistanceIsFormattingOnly(t *testing.T) {
	_, a, ok := styleVector("a.py", []byte(strings.Repeat("# comment\n", 30)))
	if !ok {
		t.Fatal("supported script rejected")
	}
	_, b, ok := styleVector("b.py", []byte(strings.Repeat("\t\t\t\tMY_VARIABLE_NAME = 12345678901234567890123456789012345678901234567890\n", 30)))
	if !ok || styleDistance(a, b) < 0.30 {
		t.Fatal("large explainable style difference not detected")
	}
	if _, _, ok = styleVector("image.png", []byte(strings.Repeat("# comment\n", 30))); ok {
		t.Fatal("unsupported format styled")
	}
	if _, _, ok = styleVector("tiny.py", []byte("x=1")); ok {
		t.Fatal("insufficient style evidence accepted")
	}
}

func TestScannerActualMachOTriageAndBenignNegative(t *testing.T) {
	engine := testEngine(t)
	s, files := testScanner(t)
	magic := []byte{0xcf, 0xfa, 0xed, 0xfe}
	inert := append(append([]byte{}, magic...), []byte(" Library/LaunchAgents/ RunAtLoad SecKeychainCopyGenericPassword /bin/sh ")...)
	os.WriteFile(filepath.Join(files, "inert-binary"), inert, 0600)
	os.WriteFile(filepath.Join(files, "benign-binary"), append(magic, []byte("hello")...), 0600)
	if err := s.Configure(true, []string{files}, engine); err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	if err := s.Scan(context.Background(), time.Now(), func(_ context.Context, e Event) error { events = append(events, e); return nil }, true); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Evidence.RuleID != "nexal_macho_persistence_credentials" || events[0].Severity != "low" {
		t.Fatalf("unexpected triage %+v", events)
	}
}
func TestScannerObservationIdentityAndExpiredQueue(t *testing.T) {
	now := time.Now()
	a := scannerEvent(now, "behavior_alert", "low", "yara_x", "rule", strings.Repeat("a", 64), "", "observation-a")
	b := scannerEvent(now, "behavior_alert", "low", "yara_x", "rule", strings.Repeat("a", 64), "", "observation-b")
	if a.EventID == b.EventID {
		t.Fatal("new observation reused an old idempotency key")
	}
	s, files := testScanner(t)
	s.Configure(true, []string{files}, "/missing-engine")
	a.ObservedAt = now.Add(-8 * 24 * time.Hour).UTC().Format(TimeLayout)
	b.ObservedAt = now.UTC().Format(TimeLayout)
	if err := s.locked(func(root *os.Root, state *ScannerState) error {
		state.Pending = []Event{a, b}
		return saveScanner(root, state)
	}); err != nil {
		t.Fatal(err)
	}
	delivered := []string{}
	s.Scan(context.Background(), now, func(_ context.Context, event Event) error { delivered = append(delivered, event.EventID); return nil }, true)
	state, _ := s.Status()
	if len(delivered) != 1 || delivered[0] != b.EventID || state.ExpiredEvents != 1 || state.PendingEvents != 0 {
		t.Fatalf("expired queue blocked later observation: %+v %v", state, delivered)
	}
	if err := s.locked(func(_ *os.Root, state *ScannerState) error {
		if len(state.ArchivedExpired) != 1 || state.ArchivedExpired[0].EventID != a.EventID {
			t.Fatal("expired evidence not retained locally")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestBaselineRejectsIncompleteWalkAndRootChangeInvalidates(t *testing.T) {
	s, files := testScanner(t)
	s.Configure(true, []string{files}, "/missing")
	for _, name := range []string{"a.py", "b.py", "c.py"} {
		os.WriteFile(filepath.Join(files, name), []byte(strings.Repeat("# comment\n", 25)), 0600)
	}
	os.Symlink("missing", filepath.Join(files, "unsafe"))
	if s.ApproveBaseline(context.Background()) == nil {
		t.Fatal("incomplete baseline silently approved")
	}
	os.Remove(filepath.Join(files, "unsafe"))
	if err := s.ApproveBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Status()
	if before.BaselineID == "" {
		t.Fatal("missing baseline")
	}
	other := filepath.Join(filepath.Dir(files), "other")
	os.Mkdir(other, 0700)
	if err := s.Configure(true, []string{other}, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Status()
	if after.BaselineID != "" {
		t.Fatal("baseline retained across different roots")
	}
	privateChild := filepath.Join(s.Directory, "child")
	os.Mkdir(privateChild, 0700)
	if s.Configure(true, []string{privateChild}, "") == nil {
		t.Fatal("private state descendant accepted")
	}
}
func TestScannerPagedWalkAndOutboxBound(t *testing.T) {
	s, files := testScanner(t)
	for i := 0; i < MaxScanFiles+1; i++ {
		f, err := os.CreateTemp(files, "entry")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	count := 0
	_, err := walkInputs(context.Background(), []string{files}, func(string, []byte) error { count++; return nil })
	if !errors.Is(err, errScanLimit) || count != MaxScanFiles {
		t.Fatalf("walk bound count=%d err=%v", count, err)
	}
	engine := testEngine(t)
	s.Configure(true, []string{files}, engine)
	s.locked(func(root *os.Root, state *ScannerState) error {
		for i := 0; i < MaxPendingEvents; i++ {
			state.Pending = append(state.Pending, scannerEvent(time.Now(), "behavior_alert", "low", "yara_x", "rule", strings.Repeat("a", 64), "", string(rune(i))))
		}
		return saveScanner(root, state)
	})
	s.Scan(context.Background(), time.Now(), nil, true)
	state, _ := s.Status()
	if state.Status != "limited" || state.PendingEvents != MaxPendingEvents || state.FilesScanned != 0 {
		t.Fatalf("outbox grew or silently discarded: %+v", state)
	}
}

func TestScanRootIdentityRejectsReplacementAndSymlinks(t *testing.T) {
	_, files := testScanner(t)
	original, err := os.Lstat(files)
	if err != nil {
		t.Fatal(err)
	}
	root, err := openScanRoot(files)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if !scanRootIdentityMatches(files, original, root) {
		t.Fatal("stable directory rejected")
	}
	moved := files + "-moved"
	if err = os.Rename(files, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(files, 0700); err != nil {
		t.Fatal(err)
	}
	if scanRootIdentityMatches(files, original, root) {
		t.Fatal("replacement directory accepted")
	}
	replacement, err := os.OpenRoot(files)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if scanRootIdentityMatches(files, original, replacement) {
		t.Fatal("opened replacement did not match captured identity")
	}
	if err = os.Remove(files); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(moved, files); err != nil {
		t.Fatal(err)
	}
	if scanRootIdentityMatches(files, original, root) {
		t.Fatal("symlink to original directory accepted")
	}
	if unsafe, err := openScanRoot(files); err == nil {
		unsafe.Close()
		t.Fatal("symlink root opened")
	}
	alias := filepath.Join(filepath.Dir(files), "parent-alias")
	if err = os.Symlink(filepath.Dir(moved), alias); err != nil {
		t.Fatal(err)
	}
	if unsafe, err := openScanRoot(filepath.Join(alias, filepath.Base(moved))); err == nil {
		unsafe.Close()
		t.Fatal("symlink ancestor opened")
	}
}

func TestParentScanExcludesPrivateState(t *testing.T) {
	s, files := testScanner(t)
	parent := filepath.Dir(files)
	if err := s.Configure(true, []string{parent}, "/missing-engine"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Directory, "secret.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	neighbor := s.Directory + "-public"
	if err := os.Mkdir(neighbor, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{files, neighbor} {
		if err := os.WriteFile(filepath.Join(dir, "public.txt"), []byte("public"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	visited := 0
	skipped, err := walkInputs(context.Background(), []string{parent, s.Directory}, func(name string, data []byte) error {
		visited++
		if string(data) != "public" {
			t.Fatalf("private input reached scanner: %s", name)
		}
		return nil
	}, s.Directory)
	if err != nil || skipped != 0 || visited != 2 {
		t.Fatalf("visited=%d skipped=%d err=%v", visited, skipped, err)
	}
	for _, name := range []string{"a.py", "b.py", "c.py"} {
		if err := os.WriteFile(filepath.Join(files, name), []byte(strings.Repeat("print('hello')\n", 25)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ApproveBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
}
