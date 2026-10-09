package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Installer is the install half of "Add AI Inference Model". It downloads a
// PINNED snapshot, verifies every byte, and writes the runtime manifest. It
// never writes owner-policy.json or runtime-config.json and never claims the
// model is runnable.

// NDJSON events (`nexal inference install --json`). Field names are a contract.
type (
	// StartEvent: totalBytes is the sum of the pinned file sizes.
	StartEvent struct {
		Event      string `json:"event"` // "start"
		ModelID    string `json:"modelId"`
		TotalBytes int64  `json:"totalBytes"`
	}
	ProgressEvent struct {
		Event        string `json:"event"` // "progress"
		File         string `json:"file"`
		Bytes        int64  `json:"bytes"`
		Total        int64  `json:"total"`
		OverallBytes int64  `json:"overallBytes"`
		OverallTotal int64  `json:"overallTotal"`
	}
	VerifiedEvent struct {
		Event string `json:"event"` // "verified"
		File  string `json:"file"`
	}
	DoneEvent struct {
		Event          string   `json:"event"` // "done"
		ModelID        string   `json:"modelId"`
		Dir            string   `json:"dir"`
		ManifestSHA256 string   `json:"manifestSha256"`
		State          string   `json:"state"`
		HowToUse       []string `json:"howToUse"`
	}
	ErrorEvent struct {
		Event   string `json:"event"` // "error"
		Code    string `json:"code"`
		Message string `json:"message"`
		Fix     string `json:"fix"`
	}
)

// ReceiptSchemaVersion is the version of install-receipt files.
const ReceiptSchemaVersion = 1

// Receipt is written OUTSIDE the model directory (inference/receipts/<id>.json)
// because the runtime rejects any undeclared file inside it.
type Receipt struct {
	SchemaVersion int    `json:"schemaVersion"`
	ModelID       string `json:"modelId"`
	State         string `json:"state"`
	// EstimatedMemory is always true for an install: the manifest's memory terms
	// are catalog estimates, not measurements.
	EstimatedMemory bool           `json:"estimatedMemory"`
	HFRepo          string         `json:"hfRepo"`
	Revision        string         `json:"revision"`
	License         string         `json:"license"`
	ModelType       string         `json:"modelType"`
	InstalledAt     string         `json:"installedAt"`
	Dir             string         `json:"dir"`
	ManifestSHA256  string         `json:"manifestSha256"`
	ManifestBytes   int64          `json:"manifestBytes"`
	Memory          ManifestMemory `json:"memory"`
	Files           []PinFile      `json:"files"`
	TotalBytes      int64          `json:"totalBytes"`
	Warnings        []string       `json:"warnings"`
	NextSteps       []string       `json:"nextSteps"`
}

// Installer holds everything an install needs; tests inject fakes.
type Installer struct {
	InferenceDir string
	Catalog      CatalogFile
	Client       *HFClient
	// Preflight returns the engine report for THIS host (memory verdicts, disk).
	Preflight func(ctx context.Context) (Report, error)
	Emit      func(ev any)
	Now       func() time.Time
	// ProgressEvery throttles progress events (default 250ms; negative = every
	// chunk, for tests).
	ProgressEvery time.Duration
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Installer) emit(ev any) {
	if in.Emit != nil {
		in.Emit(ev)
	}
}

// Layout of the inference directory.
func modelsRoot(dir string) string   { return filepath.Join(dir, "models") }
func receiptsRoot(dir string) string { return filepath.Join(dir, "receipts") }
func receiptPath(dir, id string) string {
	return filepath.Join(receiptsRoot(dir), id+".json")
}
func modelDirPath(dir, id string) string { return filepath.Join(modelsRoot(dir), id) }
func stagingPath(dir, id string) string  { return filepath.Join(modelsRoot(dir), "."+id+".partial") }

// prepareRoot creates the 0700 directory tree and returns the symlink-resolved
// inference directory (the runtime refuses model paths that traverse symlinks).
func prepareRoot(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("the inference directory must be absolute")
	}
	for _, d := range []string{dir, modelsRoot(dir), receiptsRoot(dir)} {
		if err := ensurePrivateDir(d); err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(dir)
}

// Install runs the whole flow. Any failure is also emitted as an error event.
func (in *Installer) Install(ctx context.Context, id string) (rc Receipt, err error) {
	defer func() {
		if err != nil {
			e := AsError(err)
			if ctx.Err() != nil && e.Code == "io_error" {
				e = cancelled()
			}
			err = e
			in.emit(ErrorEvent{Event: "error", Code: e.Code, Message: e.Message, Fix: e.Fix})
		}
	}()
	if !entryIDs.MatchString(id) {
		return rc, errf("invalid_arguments", "use a model id from `nexal inference preflight --json`", "invalid model id")
	}
	var entry *CatalogEntry
	for i := range in.Catalog.Entries {
		if in.Catalog.Entries[i].ID == id {
			entry = &in.Catalog.Entries[i]
		}
	}
	if entry == nil {
		return rc, errf("model_not_found", "run `nexal inference preflight --json` to list model ids", "%q is not a catalog model", id)
	}
	if !entry.Installable {
		return rc, errf("not_installable", "pin the model first: `nexal inference pin "+id+" --revision <40-hex-commit>` on a Mac with network access", "%s cannot be installed: %s", id, entry.InstallBlockedReason)
	}
	if in.Preflight == nil || in.Client == nil {
		return rc, errf("internal", "", "installer is not configured")
	}
	report, perr := in.Preflight(ctx)
	if perr != nil {
		return rc, errf("preflight_failed", "run `nexal inference preflight` and fix what it reports", "the host check failed: %v", perr)
	}
	self, herr := checkHostFit(report, *entry)
	if herr != nil {
		return rc, herr
	}

	root, err := prepareRoot(in.InferenceDir)
	if err != nil {
		return rc, err
	}
	unlock, err := lockInstall(root)
	if err != nil {
		return rc, errf("locked", "wait for the other install to finish", "another install is running")
	}
	defer unlock()

	final := modelDirPath(root, id)
	if _, serr := os.Lstat(final); serr == nil {
		if _, rerr := os.Lstat(receiptPath(root, id)); rerr == nil {
			return rc, errf("already_installed", "run `nexal inference remove "+id+" --yes` first to reinstall", "%s is already installed", id)
		}
		return rc, errf("model_dir_exists", "inspect it, then run `nexal inference remove "+id+" --yes` or move it away", "%s already exists without an install receipt", final)
	}
	stage := stagingPath(root, id)
	if err := ensurePrivateDir(stage); err != nil {
		return rc, err
	}
	if err := cleanStaging(stage, entry.Files); err != nil {
		return rc, err
	}

	var total, existing int64
	for _, f := range entry.Files {
		total += f.Size
		if st, err := os.Lstat(filepath.Join(stage, f.Name)); err == nil && st.Mode().IsRegular() {
			existing += min64(st.Size(), f.Size)
		} else if st, err := os.Lstat(filepath.Join(stage, f.Name+".part")); err == nil && st.Mode().IsRegular() {
			existing += min64(st.Size(), f.Size)
		}
	}
	if derr := checkDisk(self, total-existing); derr != nil {
		return rc, derr
	}

	in.emit(StartEvent{Event: "start", ModelID: id, TotalBytes: total})
	var base int64
	var lastEmit time.Time
	every := in.ProgressEvery
	if every == 0 {
		every = 250 * time.Millisecond
	}
	for _, f := range entry.Files {
		f := f
		onBytes := func(done int64) {
			now := in.now()
			if done != f.Size && every > 0 && !lastEmit.IsZero() && now.Sub(lastEmit) < every {
				return
			}
			lastEmit = now
			in.emit(ProgressEvent{Event: "progress", File: f.Name, Bytes: done, Total: f.Size, OverallBytes: base + done, OverallTotal: total})
		}
		if err := in.Client.DownloadFile(ctx, entry.HFRepo, entry.Revision, f, stage, onBytes); err != nil {
			return rc, err
		}
		base += f.Size
		in.emit(VerifiedEvent{Event: "verified", File: f.Name})
	}

	man, warns, err := BuildManifest(*entry)
	if err != nil {
		return rc, err
	}
	mb, err := man.Bytes()
	if err != nil {
		return rc, err
	}
	mpath := filepath.Join(stage, ManifestFileName)
	_ = os.Remove(mpath)
	if err := writeNewFile(mpath, mb, 0o400); err != nil {
		return rc, err
	}
	if err := checkModelDirExact(stage, entry.Files); err != nil {
		return rc, err
	}
	if err := os.Rename(stage, final); err != nil {
		return rc, err
	}
	msha := sha256Hex(mb)
	rc = Receipt{SchemaVersion: ReceiptSchemaVersion, ModelID: id, State: StateInstalledUnmeasured, EstimatedMemory: true,
		HFRepo: entry.HFRepo, Revision: entry.Revision, License: entry.License, ModelType: entry.Architecture,
		InstalledAt: in.now().UTC().Format(time.RFC3339), Dir: final, ManifestSHA256: msha, ManifestBytes: int64(len(mb)),
		Memory: man.Memory, Files: append([]PinFile(nil), entry.Files...), TotalBytes: total, Warnings: append([]string{}, warns...),
		NextSteps: HowToUse(final, msha)}
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return rc, err
	}
	if err := writeFileAtomic(receiptPath(root, id), append(data, '\n'), 0o600); err != nil {
		return rc, errf("receipt_failed", "run `nexal inference remove "+id+" --yes` and install again", "the files were installed but the receipt could not be written: %v", err)
	}
	in.emit(DoneEvent{Event: "done", ModelID: id, Dir: final, ManifestSHA256: msha, State: StateInstalledUnmeasured, HowToUse: rc.NextSteps})
	return rc, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// writeNewFile creates path exclusively (never following a symlink) and syncs.
func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(path, mode)
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// cleanStaging removes anything in the staging directory that the pinned file
// list does not account for (old .part files for finished files, files from a
// previous revision), so the directory can only ever hold declared files.
func cleanStaging(stage string, files []PinFile) error {
	keep := map[string]bool{}
	for _, f := range files {
		keep[f.Name] = true
		keep[f.Name+".part"] = true
	}
	ents, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if keep[e.Name()] {
			if info, err := os.Lstat(filepath.Join(stage, e.Name())); err != nil || !info.Mode().IsRegular() {
				if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
					return err
				}
			}
			continue
		}
		if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// checkModelDirExact enforces the runtime's directory rule: exactly the
// declared files plus the manifest, all regular, none group/other writable.
func checkModelDirExact(dir string, files []PinFile) error {
	want := map[string]bool{ManifestFileName: true}
	for _, f := range files {
		want[f.Name] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(ents) != len(want) {
		return errf("unexpected_file", "remove the model and install again", "the model directory does not hold exactly the declared files")
	}
	for _, e := range ents {
		st, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if !want[e.Name()] || !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 {
			return errf("unexpected_file", "remove the model and install again", "%s is not an allowed model file", e.Name())
		}
	}
	return nil
}

// checkHostFit applies the preflight verdict for THIS Mac.
func checkHostFit(r Report, e CatalogEntry) (MachineReport, *Error) {
	var self *MachineReport
	for i := range r.Machines {
		if r.Machines[i].IsSelf {
			self = &r.Machines[i]
		}
	}
	var v *ModelVerdict
	for i := range r.Models {
		if r.Models[i].ID == e.ID {
			v = &r.Models[i]
		}
	}
	if self == nil || v == nil {
		return MachineReport{}, errf("host_unknown", "open the neXal app so the connector can report this Mac, then try again", "this Mac's facts are not available (is the connector running?)")
	}
	switch v.Verdict {
	case VerdictDoesNotFit:
		return *self, errf("does_not_fit", "choose a smaller model or free memory; see `nexal inference preflight`", "%s does not fit this Mac: %s", e.DisplayName, v.Summary)
	case VerdictFitsShardedOnly:
		return *self, errf("sharded_only", "choose a model that fits one Mac; splitting across Macs cannot run yet", "%s only fits split across Macs, which cannot run yet", e.DisplayName)
	}
	if v.HostID != self.ID {
		return *self, errf("does_not_fit", "run the install on "+v.HostName+", or choose a smaller model", "%s fits %s but not this Mac", e.DisplayName, v.HostName)
	}
	return *self, nil
}

// checkDisk requires need bytes plus the owner's free-disk floor.
func checkDisk(m MachineReport, need int64) *Error {
	if need < 0 {
		need = 0
	}
	if !m.DiskKnown {
		return errf("disk_unknown", "free disk space on this Mac could not be read", "free disk space is unknown")
	}
	floor := m.DiskReserveByte
	if floor == 0 {
		floor = defaultMinFreeDiskBytes
	}
	if m.DiskFreeBytes < floor || int64(m.DiskFreeBytes-floor) < need {
		return errf("disk_low", "free disk space, or choose a smaller model",
			"%s free; the download needs %s more plus the owner's %s free-space floor", gib(int64(m.DiskFreeBytes)), gib(need), gib(int64(floor)))
	}
	return nil
}

// HowToUse is the owner's next steps. It names only commands that exist today
// (runtimes/README.md) and is honest about what does not exist.
func HowToUse(dir, manifestSHA string) []string {
	return []string{
		fmt.Sprintf("The files are installed and verified against the pin, but NOT runnable yet (state %s): the memory figures in %s are catalog estimates, and the runtime requires measured values.", StateInstalledUnmeasured, filepath.Join(dir, ManifestFileName)),
		"1. As the owner, measure load peak, resident weights, per-token KV, activations and buffers on this Mac and replace the manifest's memory block with the measured values (runtimes/README.md, \"Local provisioning\"); the manifest SHA-256 then changes, so recompute it with: shasum -a 256 " + filepath.Join(dir, ManifestFileName),
		fmt.Sprintf("2. Write an owner-managed, mode-0600 runtime-config.json (see runtimes/examples/runtime-config.json) with model_directory %s and model_manifest_sha256 set to the reviewed digest (the estimate-only manifest above is %s). nexal never writes this file.", dir, manifestSHA),
		"3. Check the model: /ABSOLUTE/APPROVED/VENV/bin/python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py verify-model --config /ABSOLUTE/OWNER/runtime-config.json",
		"4. After the owner completes the release gates (runtimes/RELEASE-GATES.md): /ABSOLUTE/APPROVED/VENV/bin/python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py infer --config /ABSOLUTE/OWNER/runtime-config.json --admission /ABSOLUTE/OWNER/attempt-admission.json --prompt-file /ABSOLUTE/OWNER/prompt.txt --max-tokens 128",
		fmt.Sprintf("Limits: prompts up to %d tokens (16 KiB), answers up to %d tokens, greedy decoding.", RuntimeMaxInputTokens, RuntimeMaxOutputTokens),
		"There is no chat or web UI for local models yet, and no multi-host execution: a model runs only from the command line on this one Mac.",
	}
}

// sortedReceiptIDs lists receipt ids (file names only; contents are not trusted).
func sortedReceiptIDs(dir string) ([]string, error) {
	ents, err := os.ReadDir(receiptsRoot(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		n := e.Name()
		if len(n) > 5 && n[len(n)-5:] == ".json" && entryIDs.MatchString(n[:len(n)-5]) && e.Type().IsRegular() {
			ids = append(ids, n[:len(n)-5])
		}
	}
	sort.Strings(ids)
	return ids, nil
}
