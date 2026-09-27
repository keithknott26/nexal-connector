package cybersecurity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"nexal/connector/internal/observability"
)

const RulesVersion = "nexal_rules_v1"
const EngineVersion = "1.20.0"
const ScanInterval = 15 * time.Minute
const MaxScanFiles = 2000
const MaxScanEntries = 10000
const MaxScanFileBytes = 4 << 20
const MaxScanBytes = 64 << 20
const MaxPendingEvents = 100

//go:embed rules/nexal.yar
var bundledRules []byte

type Scanner struct{ Directory string }

func ScannerForConfig(path string) Scanner { return Scanner{Directory: filepath.Dir(path)} }

type LocalFinding struct {
	EventID       string   `json:"eventId"`
	Path          string   `json:"path"`
	RuleID        string   `json:"ruleId"`
	Engine        string   `json:"engine"`
	ContentSHA256 string   `json:"contentSha256"`
	ObservedAt    string   `json:"observedAt"`
	Severity      string   `json:"severity"`
	TestOnly      bool     `json:"testOnly"`
	Score         *float64 `json:"score,omitempty"`
	BaselineID    string   `json:"baselineId,omitempty"`
}

type ScannerState struct {
	Enabled         bool                   `json:"enabled"`
	Status          string                 `json:"status"`
	Roots           []string               `json:"roots"`
	EnginePath      string                 `json:"enginePath"`
	EngineVersion   string                 `json:"engineVersion"`
	RulesVersion    string                 `json:"rulesVersion"`
	LastScanAt      string                 `json:"lastScanAt,omitempty"`
	LastAttemptAt   string                 `json:"lastAttemptAt,omitempty"`
	LastError       string                 `json:"lastError,omitempty"`
	FilesScanned    int                    `json:"filesScanned"`
	FilesSkipped    int                    `json:"filesSkipped"`
	Findings        int                    `json:"findings"`
	PendingEvents   int                    `json:"pendingEvents"`
	BaselineID      string                 `json:"baselineId,omitempty"`
	Coverage        string                 `json:"coverage"`
	Cache           map[string]string      `json:"cache,omitempty"`
	Baselines       map[string]StyleVector `json:"baselines,omitempty"`
	Pending         []Event                `json:"pending,omitempty"`
	LocalFindings   []LocalFinding         `json:"localFindings,omitempty"`
	ArchivedExpired []Event                `json:"archivedExpired,omitempty"`
	ExpiredEvents   int                    `json:"expiredEvents"`
}

func (s Scanner) locked(fn func(*os.Root, *ScannerState) error) error {
	parent, err := os.OpenRoot(s.Directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = parent.Mkdir("security-scanner", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	stat, err := parent.Lstat("security-scanner")
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe scanner directory")
	}
	root, err := parent.OpenRoot("security-scanner")
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if stat, err := lock.Stat(); err != nil || !stat.Mode().IsRegular() {
		return errors.New("unsafe scanner lock")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("scanner busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := ScannerState{Status: "disabled", RulesVersion: RulesVersion, Coverage: "configured_roots", Roots: []string{}, Cache: map[string]string{}}
	f, err := root.OpenFile("state.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("unsafe scanner state")
		}
		b, e := io.ReadAll(io.LimitReader(f, 8<<20))
		if e != nil || len(b) >= 8<<20 || json.Unmarshal(b, &state) != nil {
			return errors.New("invalid scanner state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Cache == nil {
		state.Cache = map[string]string{}
	}
	if len(state.Pending) > MaxPendingEvents || len(state.Roots) > 16 || len(state.Cache) > MaxScanFiles || len(state.LocalFindings) > 100 || len(state.ArchivedExpired) > 100 {
		return errors.New("invalid scanner state limits")
	}
	return fn(root, &state)
}
func saveScanner(root *os.Root, s *ScannerState) error {
	s.PendingEvents = len(s.Pending)
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) >= 8<<20 {
		return errors.New("scanner state limit")
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return err
	}
	name := "state-" + hex.EncodeToString(id) + ".tmp"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, err = f.Write(b)
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
	if err = root.Rename(name, "state.json"); err != nil {
		return err
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s Scanner) Status() (ScannerState, error) {
	var out ScannerState
	// Atomic state replacement permits a nonblocking status read during scans.
	parent, err := os.OpenRoot(s.Directory)
	if err != nil {
		return out, err
	}
	defer parent.Close()
	st, err := parent.Lstat("security-scanner")
	if errors.Is(err, os.ErrNotExist) {
		return ScannerState{Status: "disabled", Roots: []string{}, RulesVersion: RulesVersion, Coverage: "configured_roots"}, nil
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return out, errors.New("unsafe scanner state")
	}
	root, err := parent.OpenRoot("security-scanner")
	if err != nil {
		return out, err
	}
	defer root.Close()
	f, err := root.OpenFile("state.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return ScannerState{Status: "disabled", Roots: []string{}, RulesVersion: RulesVersion, Coverage: "configured_roots"}, nil
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return out, errors.New("unsafe scanner state")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil || len(raw) >= 8<<20 || json.Unmarshal(raw, &out) != nil {
		return out, errors.New("invalid scanner state")
	}
	if out.Status == "running" {
		at, _ := time.Parse(time.RFC3339, out.LastAttemptAt)
		if at.IsZero() || time.Since(at) > 2*time.Minute+15*time.Second {
			out.Status = "error"
			out.LastError = "scan_limit"
		}
	}
	out.PendingEvents = len(out.Pending)
	out.Pending = nil
	out.ArchivedExpired = nil
	out.LocalFindings = nil
	out.Cache = nil
	out.Baselines = nil
	return out, nil
}
func safeRoot(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return false
	}
	// Refuse every symlink component, including a symlinked ancestor.
	part := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part = filepath.Join(part, component)
		st, e := os.Lstat(part)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// openScanRoot binds the checked path to the directory actually opened. A
// replacement or symlink swap during open is rejected; subsequent reads stay
// confined to this verified directory descriptor even if its name is moved.
func openScanRoot(path string) (*os.Root, error) {
	if !safeRoot(path) {
		return nil, errors.New("unsafe scan root")
	}
	original, err := os.Lstat(path)
	if err != nil || !original.IsDir() || original.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe scan root")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if !scanRootIdentityMatches(path, original, root) {
		root.Close()
		return nil, errors.New("scan root changed while opening")
	}
	return root, nil
}
func scanRootIdentityMatches(path string, original os.FileInfo, root *os.Root) bool {
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(original, opened) {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(original, current) && safeRoot(path)
}

func (s Scanner) Configure(enabled bool, roots []string, engine string) error {
	if len(roots) > 16 {
		return errors.New("at most sixteen scan roots")
	}
	for _, path := range roots {
		if !safeRoot(path) {
			return errors.New("scan roots must be real absolute directories without symlinks")
		}
		if path == s.Directory || strings.HasPrefix(s.Directory, path+"/") || strings.HasPrefix(path, s.Directory+"/") {
			return errors.New("scan roots must not contain connector private state")
		}
	}
	if engine != "" && !filepath.IsAbs(engine) {
		return errors.New("engine path must be absolute")
	}
	return s.locked(func(root *os.Root, state *ScannerState) error {
		if roots != nil && !slices.Equal(state.Roots, roots) {
			state.Roots = roots
			state.Baselines = nil
			state.BaselineID = ""
			state.Cache = map[string]string{}
		}
		if engine != "" {
			state.EnginePath = engine
		}
		if state.EnginePath == "" {
			exe, e := os.Executable()
			if e != nil {
				return e
			}
			state.EnginePath = filepath.Join(filepath.Dir(exe), "yr")
		}
		if enabled && len(state.Roots) == 0 {
			return errors.New("choose at least one scan root before enabling")
		}
		state.Enabled = enabled
		state.Status = "disabled"
		state.LastError = ""
		state.LastAttemptAt = ""
		if enabled {
			state.Status = "idle"
		}
		return saveScanner(root, state)
	})
}

var errScanLimit = errors.New("scan limit reached")

// walkInputs never follows symlinks and reads through os.Root confinement. The
// scanned engine input is this bounded immutable byte snapshot, not a user path.
func walkInputs(ctx context.Context, roots []string, visit func(string, []byte) error) (int, error) {
	entries, files, total, skipped := 0, 0, 0, 0
	for _, path := range roots {
		root, err := openScanRoot(path)
		if err != nil {
			skipped++
			continue
		}
		var walk func(string, int) error
		walk = func(directory string, depth int) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			f, openErr := root.OpenFile(directory, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if openErr != nil {
				skipped++
				return nil
			}
			defer f.Close()
			for {
				batch, readErr := f.ReadDir(64)
				for _, d := range batch {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					entries++
					if entries > MaxScanEntries {
						return errScanLimit
					}
					name := filepath.Join(directory, d.Name())
					if d.Type()&os.ModeSymlink != 0 {
						skipped++
						continue
					}
					if d.IsDir() {
						if depth >= 16 {
							skipped++
							continue
						}
						if e := walk(name, depth+1); e != nil {
							return e
						}
						continue
					}
					if !d.Type().IsRegular() {
						skipped++
						continue
					}
					files++
					if files > MaxScanFiles {
						return errScanLimit
					}
					unsafe := false
					prefix := ""
					for _, part := range strings.Split(name, "/") {
						prefix = filepath.Join(prefix, part)
						st, e := root.Lstat(prefix)
						if e != nil || st.Mode()&os.ModeSymlink != 0 {
							unsafe = true
							break
						}
					}
					if unsafe {
						skipped++
						continue
					}
					input, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
					if e != nil {
						skipped++
						continue
					}
					st, e := input.Stat()
					if e != nil || !st.Mode().IsRegular() || st.Size() > MaxScanFileBytes {
						input.Close()
						skipped++
						continue
					}
					data, e := io.ReadAll(io.LimitReader(input, MaxScanFileBytes+1))
					input.Close()
					if e != nil || len(data) > MaxScanFileBytes {
						skipped++
						continue
					}
					total += len(data)
					if total > MaxScanBytes {
						return errScanLimit
					}
					if e = visit(filepath.Join(path, name), data); e != nil {
						return e
					}
				}
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				if readErr != nil {
					skipped++
					return nil
				}
			}
		}
		err = walk(".", 0)
		root.Close()
		if err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s Scanner) ApproveBaseline(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return s.locked(func(root *os.Root, state *ScannerState) error {
		if len(state.Roots) == 0 {
			return errors.New("configure scan roots first")
		}
		sums := map[string]StyleVector{}
		counts := map[string]int{}
		skipped, err := walkInputs(ctx, state.Roots, func(name string, data []byte) error {
			ext, v, ok := styleVector(name, data)
			if ok {
				sum := sums[ext]
				for i := range sum {
					sum[i] += v[i]
				}
				sums[ext] = sum
				counts[ext]++
			}
			return nil
		})
		if err != nil || skipped > 0 {
			return errors.New("baseline scan incomplete; nothing approved")
		}
		approved := map[string]StyleVector{}
		for ext, sum := range sums {
			if counts[ext] >= 3 {
				for i := range sum {
					sum[i] /= float64(counts[ext])
				}
				approved[ext] = sum
			}
		}
		if len(approved) == 0 {
			return errors.New("baseline requires at least three scripts of one language with twenty nonempty lines each")
		}
		raw, _ := json.Marshal(approved)
		state.Baselines = approved
		state.BaselineID = digest(append([]byte(StyleVersion), raw...))
		state.Cache = map[string]string{}
		return saveScanner(root, state)
	})
}

// Scan persists each finding before marking its content inspected. If delivery
// fails, a later pass retries the identical event; a full outbox stops progress.
func (s Scanner) Scan(ctx context.Context, now time.Time, report func(context.Context, Event) error, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return s.locked(func(root *os.Root, state *ScannerState) error {
		if !state.Enabled {
			return nil
		}
		if !force {
			at, _ := time.Parse(time.RFC3339, state.LastAttemptAt)
			if !at.IsZero() && now.Sub(at) < ScanInterval {
				return nil
			}
		}
		state.LastAttemptAt = now.UTC().Format(time.RFC3339)
		state.Status = "running"
		state.LastError = ""
		if err := saveScanner(root, state); err != nil {
			return err
		}
		scanOutcome := "state_error"
		fail := func(code string) error {
			scanOutcome = code
			if code == "scan_limit" {
				scanOutcome = "limited"
			}
			state.LastError = code
			state.Status = "error"
			if code == "engine_unavailable" {
				state.Status = code
			}
			if code == "scan_limit" {
				state.Status = "limited"
			}
			return saveScanner(root, state)
		}
		// Delivery happens before new scanning. Old queued observations age out of
		// the server's seven-day contract; retain them locally and surface pending.
		// Expired observations remain in a bounded local archive; they cannot
		// poison delivery of all later findings through the API's time window.
		retained := state.Pending[:0]
		for _, event := range state.Pending {
			at, err := time.Parse(TimeLayout, event.ObservedAt)
			if err != nil || at.Before(now.Add(-7*24*time.Hour)) {
				state.ArchivedExpired = append(state.ArchivedExpired, event)
				if len(state.ArchivedExpired) > MaxPendingEvents {
					state.ArchivedExpired = state.ArchivedExpired[len(state.ArchivedExpired)-MaxPendingEvents:]
				}
				if state.ExpiredEvents < 1000000 {
					state.ExpiredEvents++
				}
			} else {
				retained = append(retained, event)
			}
		}
		state.Pending = retained
		if err := saveScanner(root, state); err != nil {
			return err
		}
		observationBytes := make([]byte, 16)
		if _, err := rand.Read(observationBytes); err != nil {
			return fail("state_error")
		}
		observationID := hex.EncodeToString(observationBytes)
		if report != nil {
			for len(state.Pending) > 0 {
				request, stop := context.WithTimeout(ctx, 10*time.Second)
				err := report(request, state.Pending[0])
				stop()
				if err != nil {
					state.LastError = "delivery_pending"
					break
				}
				state.Pending = state.Pending[1:]
				if err = saveScanner(root, state); err != nil {
					return err
				}
			}
		}
		finishTelemetry := observability.ThreatScan(ctx)
		defer func() { finishTelemetry(scanOutcome) }()
		engineHash, err := checkEngine(ctx, state.EnginePath)
		if err != nil {
			return fail("engine_unavailable")
		}
		state.EngineVersion = EngineVersion
		state.RulesVersion = RulesVersion
		// Snapshots are private, randomly named, transient, and never uploaded.
		temp, err := os.MkdirTemp(filepath.Join(s.Directory, "security-scanner"), "scan-")
		if err != nil {
			return fail("state_error")
		}
		defer os.RemoveAll(temp)
		rulesPath := filepath.Join(temp, "rules.yar")
		compiled := filepath.Join(temp, "rules.yarc")
		target := filepath.Join(temp, "input.bin")
		if os.WriteFile(rulesPath, bundledRules, 0600) != nil {
			return fail("state_error")
		}
		if _, err = engineCommand(ctx, state.EnginePath, "compile", "--output", compiled, rulesPath); err != nil {
			return fail("engine_failed")
		}
		scanFiles, scanFindings := 0, 0
		seen := map[string]bool{}
		oldCache := state.Cache
		oldContent := map[string]bool{}
		for _, token := range oldCache {
			oldContent[token] = true
		}
		state.Cache = map[string]string{}
		skipped, err := walkInputs(ctx, state.Roots, func(name string, data []byte) error {
			pathID := digest([]byte(name))
			seen[pathID] = true
			content := digest(data)
			key := digest([]byte(content + engineHash + digest(bundledRules) + StyleVersion + state.BaselineID + strings.ToLower(filepath.Ext(name))))
			if oldContent[key] {
				// Keep a locally reviewed location useful after a rename. Never upload it.
				for i := range state.LocalFindings {
					finding := &state.LocalFindings[i]
					if finding.ContentSHA256 == content && finding.Path != name {
						if _, err := os.Lstat(finding.Path); errors.Is(err, os.ErrNotExist) {
							finding.Path = name
						}
					}
				}
				state.Cache[pathID] = key
				return nil
			}
			if len(state.Pending) >= MaxPendingEvents {
				return errScanLimit
			}
			if err := os.WriteFile(target, data, 0600); err != nil {
				return errors.New("state_error")
			}
			rules, err := scanBytes(ctx, state.EnginePath, compiled, target)
			if err != nil {
				return err
			}
			scanFiles++
			var events []Event
			for _, rule := range rules {
				severity := "low"
				test := rule == "nexal_eicar_test"
				if test {
					severity = "info"
				}
				ev := scannerEvent(now, "behavior_alert", severity, "yara_x", rule, content, state.BaselineID, observationID)
				ev.Evidence = &Evidence{Engine: "yara_x", RuleID: rule, RulesVersion: RulesVersion, ContentSHA256: content, TestOnly: test}
				events = append(events, ev)
			}
			if ext, v, ok := styleVector(name, data); ok {
				if baseline, exists := state.Baselines[ext]; exists {
					score := styleDistance(v, baseline)
					if score >= 0.30 {
						ev := scannerEvent(now, "code_style_signal", "info", "nexal_style", "style_baseline_deviation", content, state.BaselineID, observationID)
						ev.OriginAssessment = "insufficient_evidence"
						ev.Evidence = &Evidence{Engine: "nexal_style", RuleID: "style_baseline_deviation", RulesVersion: StyleVersion, ContentSHA256: content, BaselineID: state.BaselineID, Score: &score}
						events = append(events, ev)
					}
				}
			}
			for _, event := range events {
				if event.Validate(now) != nil {
					return errors.New("state_error")
				}
				duplicate := false
				for _, pending := range state.Pending {
					if pending.EventID == event.EventID {
						duplicate = true
						break
					}
				}
				if duplicate {
					continue
				}
				if len(state.Pending) >= MaxPendingEvents {
					return errScanLimit
				}
				state.Pending = append(state.Pending, event)
				state.LocalFindings = append(state.LocalFindings, LocalFinding{EventID: event.EventID, Path: name, RuleID: event.Evidence.RuleID, Engine: event.Evidence.Engine, ContentSHA256: content, ObservedAt: event.ObservedAt, Severity: event.Severity, TestOnly: event.Evidence.TestOnly, Score: event.Evidence.Score, BaselineID: event.Evidence.BaselineID})
				if len(state.LocalFindings) > 100 {
					state.LocalFindings = state.LocalFindings[len(state.LocalFindings)-100:]
				}
				scanFindings++
			}
			state.Cache[pathID] = key
			oldContent[key] = true
			return saveScanner(root, state)
		})
		if err != nil {
			if errors.Is(err, errScanLimit) || ctx.Err() != nil {
				return fail("scan_limit")
			}
			return fail("engine_failed")
		}
		for key := range state.Cache {
			if !seen[key] {
				delete(state.Cache, key)
			}
		}
		state.FilesScanned = scanFiles
		state.FilesSkipped = skipped
		state.Findings = scanFindings
		state.LastScanAt = time.Now().UTC().Format(time.RFC3339)
		state.Status = "idle"
		if skipped > 0 {
			state.Status = "limited"
			state.LastError = "scan_limit"
		}
		if err := saveScanner(root, state); err != nil {
			return err
		}
		scanOutcome = "completed"
		if skipped > 0 {
			scanOutcome = "limited"
		}
		finishTelemetry(scanOutcome)
		if report != nil {
			for len(state.Pending) > 0 {
				request, stop := context.WithTimeout(ctx, 10*time.Second)
				err := report(request, state.Pending[0])
				stop()
				if err != nil {
					state.LastError = "delivery_pending"
					break
				}
				state.Pending = state.Pending[1:]
				if err = saveScanner(root, state); err != nil {
					return err
				}
			}
		}
		if len(state.Pending) > 0 {
			state.LastError = "delivery_pending"
		}
		return saveScanner(root, state)
	})
}
func scannerEvent(now time.Time, kind, severity, engine, rule, content, baseline, observationID string) Event {
	version := "yara_x_" + strings.ReplaceAll(EngineVersion, ".", "_")
	if engine == "nexal_style" {
		version = StyleVersion
	}
	id := digest([]byte(engine + "_" + RulesVersion + "_" + StyleVersion + "_" + rule + "_" + content + "_" + baseline + "_" + observationID))
	return Event{SchemaVersion: 1, EventID: "scan_" + id, ObservedAt: now.UTC().Format(TimeLayout), Kind: kind, Severity: severity, Detector: engine, DetectorVersion: version, OriginAssessment: "unknown", EvidenceRef: "scan_" + id}
}

// Findings is an explicitly local-only disclosure, never used in host.info or
// ReportSecurityEvent. The ledger is bounded and contains no source bytes.
func (s Scanner) Findings() ([]LocalFinding, error) {
	out := []LocalFinding{}
	err := s.locked(func(_ *os.Root, state *ScannerState) error {
		for i := len(state.LocalFindings) - 1; i >= 0; i-- {
			out = append(out, state.LocalFindings[i])
		}
		return nil
	})
	return out, err
}
