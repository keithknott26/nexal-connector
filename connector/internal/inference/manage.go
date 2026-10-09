package inference

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadReceipt reads and strictly decodes an install receipt. The receipt is the
// source of truth for what should be on disk; it does not make the files
// trustworthy, verify re-hashes them.
func LoadReceipt(inferenceDir, id string) (Receipt, error) {
	var rc Receipt
	if !entryIDs.MatchString(id) {
		return rc, errf("invalid_arguments", "", "invalid model id")
	}
	p := receiptPath(inferenceDir, id)
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return rc, errf("not_installed", "run `nexal inference status` to list installed models", "%s has no install receipt", id)
	}
	if err != nil {
		return rc, err
	}
	if !st.Mode().IsRegular() || st.Size() > 4<<20 {
		return rc, errf("receipt_invalid", "", "the receipt for %s is not a regular file", id)
	}
	f, err := os.Open(p)
	if err != nil {
		return rc, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return rc, err
	}
	if err := DecodeStrict(data, &rc); err != nil {
		return Receipt{}, errf("receipt_invalid", "remove the model and install it again", "the receipt for %s is not valid: %v", id, err)
	}
	if rc.SchemaVersion != ReceiptSchemaVersion || rc.ModelID != id || checkPinFiles(rc.Files) != nil {
		return Receipt{}, errf("receipt_invalid", "remove the model and install it again", "the receipt for %s is not valid", id)
	}
	return rc, nil
}

// FileCheck is one file's result.
type FileCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | missing | size_mismatch | hash_mismatch | not_regular | unexpected | bad_mode | unchecked
}

// VerifyResult is `nexal inference verify <id> --json`.
type VerifyResult struct {
	SchemaVersion int         `json:"schemaVersion"`
	ModelID       string      `json:"modelId"`
	OK            bool        `json:"ok"`
	Full          bool        `json:"full"`
	State         string      `json:"state"`
	Revision      string      `json:"revision"`
	Files         []FileCheck `json:"files"`
	Problems      []string    `json:"problems"`
}

// CheckModel compares the model directory with its receipt. full=false checks
// presence, type, mode and size only (cheap); full=true also re-hashes every
// file and the manifest.
func CheckModel(ctx context.Context, inferenceDir, id string, full bool) (VerifyResult, error) {
	res := VerifyResult{SchemaVersion: 1, ModelID: id, Full: full, Files: []FileCheck{}, Problems: []string{}}
	rc, err := LoadReceipt(inferenceDir, id)
	if err != nil {
		return res, err
	}
	res.Revision = rc.Revision
	dir := modelDirPath(inferenceDir, id)
	add := func(name, status string) {
		res.Files = append(res.Files, FileCheck{Name: name, Status: status})
		if status != "ok" && status != "unchecked" {
			res.Problems = append(res.Problems, name+": "+strings.ReplaceAll(status, "_", " "))
		}
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		res.Problems = append(res.Problems, "the model directory is missing or not a directory")
		res.State = StateDamaged
		return res, nil
	}
	declared := map[string]bool{ManifestFileName: true}
	items := append([]PinFile(nil), rc.Files...)
	items = append(items, PinFile{Name: ManifestFileName, Size: rc.ManifestBytes, SHA256: rc.ManifestSHA256})
	for _, f := range items {
		declared[f.Name] = true
		p := filepath.Join(dir, f.Name)
		st, err := os.Lstat(p)
		switch {
		case err != nil:
			add(f.Name, "missing")
			continue
		case !st.Mode().IsRegular():
			add(f.Name, "not_regular")
			continue
		case st.Mode().Perm()&0o022 != 0:
			add(f.Name, "bad_mode")
			continue
		case st.Size() != f.Size:
			add(f.Name, "size_mismatch")
			continue
		}
		if !full {
			add(f.Name, "ok")
			continue
		}
		sum, err := hashFile(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return res, cancelled()
			}
			add(f.Name, "missing")
			continue
		}
		if sum != f.SHA256 {
			add(f.Name, "hash_mismatch")
			continue
		}
		add(f.Name, "ok")
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if !declared[e.Name()] {
				add(e.Name(), "unexpected")
			}
		}
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Name < res.Files[j].Name })
	res.OK = len(res.Problems) == 0
	res.State = rc.State
	if !res.OK {
		res.State = StateDamaged
	}
	return res, nil
}

// StatusModel is one installed model in `nexal inference status`.
type StatusModel struct {
	ModelID         string   `json:"modelId"`
	DisplayName     string   `json:"displayName,omitempty"`
	State           string   `json:"state"`
	Revision        string   `json:"revision"`
	HFRepo          string   `json:"hfRepo"`
	License         string   `json:"license"`
	BytesOnDisk     int64    `json:"bytesOnDisk"`
	InstalledAt     string   `json:"installedAt"`
	Dir             string   `json:"dir"`
	ManifestSHA256  string   `json:"manifestSha256"`
	EstimatedMemory bool     `json:"estimatedMemory"`
	Problems        []string `json:"problems"`
}

// StatusIncomplete is a partial download left by an interrupted install.
type StatusIncomplete struct {
	ModelID     string `json:"modelId"`
	Dir         string `json:"dir"`
	BytesOnDisk int64  `json:"bytesOnDisk"`
}

// StatusReport is `nexal inference status --json`.
type StatusReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	InferenceDir  string             `json:"inferenceDir"`
	Models        []StatusModel      `json:"models"`
	Incomplete    []StatusIncomplete `json:"incomplete"`
}

func dirBytes(dir string) int64 {
	var n int64
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if st, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && st.Mode().IsRegular() {
			n += st.Size()
		}
	}
	return n
}

// Status lists installed models from their receipts with a cheap size check.
// names optionally maps model id to display name.
func Status(ctx context.Context, inferenceDir string, names map[string]string) (StatusReport, error) {
	rep := StatusReport{SchemaVersion: 1, InferenceDir: inferenceDir, Models: []StatusModel{}, Incomplete: []StatusIncomplete{}}
	ids, err := sortedReceiptIDs(inferenceDir)
	if err != nil {
		return rep, err
	}
	for _, id := range ids {
		rc, err := LoadReceipt(inferenceDir, id)
		if err != nil {
			rep.Models = append(rep.Models, StatusModel{ModelID: id, State: StateDamaged, Problems: []string{AsError(err).Message}, DisplayName: names[id]})
			continue
		}
		chk, err := CheckModel(ctx, inferenceDir, id, false)
		if err != nil {
			return rep, err
		}
		probs := chk.Problems
		rep.Models = append(rep.Models, StatusModel{ModelID: id, DisplayName: names[id], State: chk.State, Revision: rc.Revision, HFRepo: rc.HFRepo,
			License: rc.License, BytesOnDisk: dirBytes(modelDirPath(inferenceDir, id)), InstalledAt: rc.InstalledAt,
			Dir: modelDirPath(inferenceDir, id), ManifestSHA256: rc.ManifestSHA256, EstimatedMemory: rc.EstimatedMemory, Problems: probs})
	}
	if ents, err := os.ReadDir(modelsRoot(inferenceDir)); err == nil {
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".partial") && e.IsDir() {
				id := strings.TrimSuffix(strings.TrimPrefix(n, "."), ".partial")
				if entryIDs.MatchString(id) {
					p := filepath.Join(modelsRoot(inferenceDir), n)
					rep.Incomplete = append(rep.Incomplete, StatusIncomplete{ModelID: id, Dir: p, BytesOnDisk: dirBytes(p)})
				}
			}
		}
	}
	return rep, nil
}

// RemoveResult is `nexal inference remove --json`.
type RemoveResult struct {
	SchemaVersion int      `json:"schemaVersion"`
	ModelID       string   `json:"modelId"`
	Removed       []string `json:"removed"`
}

// Remove deletes a model directory, any partial download and its receipt. It
// only ever deletes <inferenceDir>/models/<id>, <inferenceDir>/models/.<id>.partial
// and <inferenceDir>/receipts/<id>.json, and refuses to follow or delete through
// a symlink.
func Remove(inferenceDir, id string, yes bool) (RemoveResult, error) {
	res := RemoveResult{SchemaVersion: 1, ModelID: id, Removed: []string{}}
	if !entryIDs.MatchString(id) {
		return res, errf("invalid_arguments", "", "invalid model id")
	}
	if !yes {
		return res, errf("confirmation_required", "run again with --yes to delete "+id, "removing a model deletes its files and receipt")
	}
	if !filepath.IsAbs(inferenceDir) {
		return res, errf("invalid_arguments", "", "the inference directory must be absolute")
	}
	mroot := modelsRoot(inferenceDir)
	if st, err := os.Lstat(mroot); err != nil || !st.IsDir() {
		return res, errf("not_installed", "", "there is no models directory")
	}
	targets := []string{modelDirPath(inferenceDir, id), stagingPath(inferenceDir, id)}
	for _, t := range targets {
		rel, err := filepath.Rel(mroot, t)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
			return res, errf("path_escape", "", "refusing to remove outside the models directory")
		}
		st, err := os.Lstat(t)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return res, errf("path_escape", "inspect and remove it by hand", "%s is not a plain directory (symlinks are never followed)", filepath.Base(t))
		}
		if err := os.RemoveAll(t); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, t)
	}
	rp := receiptPath(inferenceDir, id)
	if st, err := os.Lstat(rp); err == nil {
		if !st.Mode().IsRegular() {
			return res, errf("path_escape", "", "the receipt is not a regular file")
		}
		if err := os.Remove(rp); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, rp)
	}
	if len(res.Removed) == 0 {
		return res, errf("not_installed", "run `nexal inference status` to list installed models", "%s is not installed", id)
	}
	return res, nil
}
