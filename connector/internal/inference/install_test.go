package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// runtimeVerify mirrors runtimes/nexal_mlx/security.py verify_model (without
// the config/tokenizer semantics, which pin checks): it is the contract the
// installed directory must satisfy.
func runtimeVerify(t *testing.T, dir, manifestSHA string) {
	t.Helper()
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0o022 != 0 {
		t.Fatalf("model directory must be owner-controlled: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if shaHex(raw) != manifestSHA {
		t.Fatal("manifest digest mismatch")
	}
	if err := CheckNoDuplicateKeys(raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range m {
		keys[k] = true
	}
	want := []string{"schema_version", "model_id", "revision", "license", "model_type", "files", "memory"}
	if len(keys) != len(want) {
		t.Fatalf("manifest keys = %v", keys)
	}
	for _, k := range want {
		if !keys[k] {
			t.Fatalf("missing key %s", k)
		}
	}
	var man struct {
		SchemaVersion int               `json:"schema_version"`
		ModelID       string            `json:"model_id"`
		Revision      string            `json:"revision"`
		License       string            `json:"license"`
		ModelType     string            `json:"model_type"`
		Files         map[string]string `json:"files"`
	}
	json.Unmarshal(raw, &man)
	if man.SchemaVersion != 1 || (man.ModelType != "qwen3" && man.ModelType != "phi3" && man.ModelType != "llama") {
		t.Fatal("schema/model_type")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(man.Revision) || man.ModelID == "" || man.License == "" || len(man.License) > 200 {
		t.Fatal("identity fields")
	}
	if len(man.Files) < 3 || len(man.Files) > 2048 {
		t.Fatal("files count")
	}
	ents, _ := os.ReadDir(dir)
	actual := map[string]bool{}
	for _, e := range ents {
		actual[e.Name()] = true
	}
	exp := map[string]bool{ManifestFileName: true}
	for n := range man.Files {
		exp[n] = true
	}
	if fmt.Sprint(actual) != fmt.Sprint(exp) {
		t.Fatalf("directory holds %v, manifest declares %v", actual, exp)
	}
	var weightSize int64
	hasWeights := false
	for name, sum := range man.Files {
		if !AllowedModelFile(name) {
			t.Fatalf("file %s not allowlisted", name)
		}
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
			t.Fatalf("%s: not a safe regular file", name)
		}
		got, _ := hashFile(context.Background(), filepath.Join(dir, name))
		if got != sum {
			t.Fatalf("%s: integrity", name)
		}
		if IsWeightFile(name) {
			hasWeights = true
			weightSize += fi.Size()
		}
	}
	for _, need := range []string{"config.json", "tokenizer_config.json", "tokenizer.json"} {
		if _, ok := man.Files[need]; !ok {
			t.Fatalf("lacks %s", need)
		}
	}
	if !hasWeights {
		t.Fatal("no weights")
	}
	var mem map[string]json.RawMessage
	json.Unmarshal(m["memory"], &mem)
	if len(mem) != 6 {
		t.Fatalf("memory keys = %v", mem)
	}
	val := map[string]int64{}
	for _, k := range []string{"weights_bytes", "kv_bytes_per_token", "activation_bytes", "buffer_bytes", "load_peak_bytes", "safety_bytes"} {
		var n int64
		if err := json.Unmarshal(mem[k], &n); err != nil || n < 1 {
			t.Fatalf("memory %s = %s", k, mem[k])
		}
		val[k] = n
	}
	if val["weights_bytes"] < weightSize || val["load_peak_bytes"] < val["weights_bytes"] {
		t.Fatal("memory understates the weights or load peak")
	}
}

type recorder struct{ events []any }

func (r *recorder) emit(ev any) { r.events = append(r.events, ev) }

func (r *recorder) lines(t *testing.T) []map[string]any {
	var out []map[string]any
	for _, e := range r.events {
		b, _ := json.Marshal(e)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func fitReport(verdict Verdict, hostID string, diskFree uint64, diskKnown bool) func(context.Context) (Report, error) {
	return func(context.Context) (Report, error) {
		return Report{
			Machines: []MachineReport{{ID: "self", Name: "MacBook", IsSelf: true, DiskFreeBytes: diskFree, DiskKnown: diskKnown, DiskReserveByte: 1 << 30}},
			Models:   []ModelVerdict{{ID: testID, Verdict: verdict, HostID: hostID, HostName: "Mini", Summary: "because"}},
		}, nil
	}
}

func newInstaller(t *testing.T, f *fakeHF) (*Installer, *recorder, string) {
	dir := filepath.Join(t.TempDir(), "inference")
	rec := &recorder{}
	return &Installer{InferenceDir: dir, Catalog: f.pinnedCatalog(t), Client: f.client(),
		Preflight: fitReport(VerdictFitsSingleBlocked, "self", 100<<30, true), Emit: rec.emit,
		Now: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }, ProgressEvery: -1}, rec, dir
}

func TestInstallEndToEnd(t *testing.T) {
	f := newFakeHF(t)
	in, rec, dir := newInstaller(t, f)
	rc, err := in.Install(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(dir)
	model := filepath.Join(root, "models", testID)
	if rc.Dir != model || rc.State != StateInstalledUnmeasured || !rc.EstimatedMemory || rc.Revision != testRev || rc.HFRepo != testRepo {
		t.Fatalf("receipt = %+v", rc)
	}
	runtimeVerify(t, model, rc.ManifestSHA256)

	// Permissions: dirs 0700, model files 0400.
	for _, d := range []string{root, filepath.Join(root, "models"), filepath.Join(root, "receipts"), model} {
		if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v", d, st.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(model)
	if len(ents) != 5 {
		t.Errorf("model dir holds %d entries", len(ents))
	}
	for _, e := range ents {
		if st, _ := os.Stat(filepath.Join(model, e.Name())); st.Mode().Perm() != 0o400 {
			t.Errorf("%s mode %v", e.Name(), st.Mode().Perm())
		}
	}
	// The receipt lives outside the model dir, mode 0600, and round-trips.
	rp := filepath.Join(root, "receipts", testID+".json")
	if st, err := os.Stat(rp); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("receipt file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(model, "install-receipt.json")); err == nil {
		t.Error("receipt inside the model dir")
	}
	got, err := LoadReceipt(root, testID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(rc)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("receipt round trip differs:\n%s\n%s", a, b)
	}
	if got.InstalledAt != "2026-10-09T12:00:00Z" || got.ManifestSHA256 != shaHex(mustRead(t, filepath.Join(model, ManifestFileName))) {
		t.Errorf("receipt fields: %+v", got)
	}
	// Owner-managed files are never written.
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if n := d.Name(); n == "owner-policy.json" || n == "runtime-config.json" {
			t.Errorf("wrote owner file %s", p)
		}
		return nil
	})
	// Memory terms: positive, and the manifest says the weights are at least the files.
	if rc.Memory.WeightsBytes < 440000 || rc.Memory.KVBytesPerToken != 2*36*8*128*2 {
		t.Errorf("memory = %+v", rc.Memory)
	}

	// Events: start, then per file progress* + verified, then done; nothing after.
	lines := rec.lines(t)
	if lines[0]["event"] != "start" || lines[0]["modelId"] != testID || lines[0]["totalBytes"].(float64) != float64(rc.TotalBytes) {
		t.Fatalf("start = %v", lines[0])
	}
	last := lines[len(lines)-1]
	if last["event"] != "done" || last["state"] != "installed_unmeasured" || last["dir"] != model || last["manifestSha256"] != rc.ManifestSHA256 {
		t.Fatalf("done = %v", last)
	}
	if hu, _ := last["howToUse"].([]any); len(hu) < 5 {
		t.Errorf("howToUse = %v", last["howToUse"])
	}
	verified := map[string]bool{}
	var prevOverall float64
	for i, l := range lines[1 : len(lines)-1] {
		switch l["event"] {
		case "progress":
			file := l["file"].(string)
			if verified[file] {
				t.Errorf("progress for %s after verified (line %d)", file, i)
			}
			if l["bytes"].(float64) > l["total"].(float64) || l["overallBytes"].(float64) < prevOverall || l["overallTotal"].(float64) != float64(rc.TotalBytes) {
				t.Errorf("bad progress %v", l)
			}
			prevOverall = l["overallBytes"].(float64)
		case "verified":
			verified[l["file"].(string)] = true
		default:
			t.Errorf("unexpected event %v", l)
		}
	}
	if len(verified) != 4 || prevOverall != float64(rc.TotalBytes) {
		t.Errorf("verified=%v overall=%v", verified, prevOverall)
	}
	// Exact field sets per event (a contract with the Swift side).
	wantKeys := map[string][]string{
		"start":    {"event", "modelId", "totalBytes"},
		"progress": {"event", "file", "bytes", "total", "overallBytes", "overallTotal"},
		"verified": {"event", "file"},
		"done":     {"event", "modelId", "dir", "manifestSha256", "state", "howToUse"},
	}
	for _, l := range lines {
		ev := l["event"].(string)
		if len(l) != len(wantKeys[ev]) {
			t.Errorf("%s has fields %v", ev, l)
		}
		for _, k := range wantKeys[ev] {
			if _, ok := l[k]; !ok {
				t.Errorf("%s lacks %s", ev, k)
			}
		}
	}
	// howToUse names only things that exist, and is honest.
	text := strings.Join(rc.NextSteps, "\n")
	for _, want := range []string{"verify-model", "infer --config", "NOT runnable", "estimates", "no chat or web UI", "no multi-host"} {
		if !strings.Contains(text, want) {
			t.Errorf("howToUse lacks %q", want)
		}
	}
	for _, bad := range []string{"nexal chat", "nexal run-model", "localhost", "http"} {
		if strings.Contains(text, bad) {
			t.Errorf("howToUse invents %q", bad)
		}
	}
	// A second install is refused, and reinstall needs remove.
	_, err = in.Install(context.Background(), testID)
	wantCode(t, err, "already_installed")
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallProgressThrottle(t *testing.T) {
	f := newFakeHF(t)
	in, rec, _ := newInstaller(t, f)
	in.ProgressEvery = time.Hour // everything but each file's last chunk is dropped after the first
	var tick int64
	in.Now = func() time.Time { return time.Unix(0, atomic.AddInt64(&tick, 1)) }
	if _, err := in.Install(context.Background(), testID); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range rec.lines(t) {
		if l["event"] == "progress" {
			n++
			if l["file"] != "" && l["bytes"] != l["total"] && n > 1 {
				t.Errorf("throttled stream let a mid-file event through: %v", l)
			}
		}
	}
	if n < 4 {
		t.Errorf("each file must end with a final progress event, got %d", n)
	}
}

func TestInstallGating(t *testing.T) {
	f := newFakeHF(t)
	tests := []struct {
		name    string
		prep    func(in *Installer)
		code    string
		touches bool // may the network be used?
	}{
		{"unpinned model", func(in *Installer) { in.Catalog = baseCatalog(t) }, "not_installable", false},
		{"unknown model", func(in *Installer) { in.Catalog.Entries = nil }, "model_not_found", false},
		{"does not fit", func(in *Installer) { in.Preflight = fitReport(VerdictDoesNotFit, "", 100<<30, true) }, "does_not_fit", false},
		{"sharded only", func(in *Installer) { in.Preflight = fitReport(VerdictFitsShardedOnly, "", 100<<30, true) }, "sharded_only", false},
		{"fits only on a peer", func(in *Installer) { in.Preflight = fitReport(VerdictFitsSingleBlocked, "mini", 100<<30, true) }, "does_not_fit", false},
		{"disk below the owner floor", func(in *Installer) { in.Preflight = fitReport(VerdictRunsNow, "self", 1<<29, true) }, "disk_low", false},
		{"disk just short", func(in *Installer) { in.Preflight = fitReport(VerdictRunsNow, "self", (1<<30)+300000, true) }, "disk_low", false},
		{"disk unknown", func(in *Installer) { in.Preflight = fitReport(VerdictRunsNow, "self", 0, false) }, "disk_unknown", false},
		{"host unknown", func(in *Installer) {
			in.Preflight = func(context.Context) (Report, error) { return Report{}, nil }
		}, "host_unknown", false},
		{"preflight error", func(in *Installer) {
			in.Preflight = func(context.Context) (Report, error) { return Report{}, errors.New("boom") }
		}, "preflight_failed", false},
		{"bad id", nil, "invalid_arguments", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in, rec, dir := newInstaller(t, f)
			id := testID
			if tc.prep != nil {
				tc.prep(in)
			} else {
				id = "../evil"
			}
			before := len(f.requests())
			_, err := in.Install(context.Background(), id)
			wantCode(t, err, tc.code)
			if len(f.requests()) != before {
				t.Error("network used before the checks passed")
			}
			lines := rec.lines(t)
			if len(lines) != 1 || lines[0]["event"] != "error" || lines[0]["code"] != tc.code || lines[0]["message"] == "" || lines[0]["fix"] == "" && tc.code != "preflight_failed" {
				t.Errorf("events = %v", lines)
			}
			if _, err := os.Stat(filepath.Join(dir, "models", testID)); err == nil {
				t.Error("model dir created")
			}
		})
	}
}

func TestInstallHashMismatchLeavesNoUnverifiedFile(t *testing.T) {
	f := newFakeHF(t)
	in, rec, dir := newInstaller(t, f)
	f.setFile("model.safetensors", append([]byte("X"), f.files["model.safetensors"][1:]...)) // same size, other bytes
	_, err := in.Install(context.Background(), testID)
	wantCode(t, err, "hash_mismatch")
	root, _ := filepath.EvalSymlinks(dir)
	if _, err := os.Stat(filepath.Join(root, "models", testID)); err == nil {
		t.Error("model dir exists")
	}
	if _, err := os.Stat(filepath.Join(root, "receipts", testID+".json")); err == nil {
		t.Error("receipt exists")
	}
	stage := filepath.Join(root, "models", "."+testID+".partial")
	for _, n := range dirNames(t, stage) {
		if n == "model.safetensors" || n == "model.safetensors.part" || n == ManifestFileName {
			t.Errorf("%s left in staging", n)
		}
	}
	lines := rec.lines(t)
	last := lines[len(lines)-1]
	if last["event"] != "error" || last["code"] != "hash_mismatch" {
		t.Errorf("last event = %v", last)
	}
	for _, l := range lines {
		if l["event"] == "done" {
			t.Error("done emitted")
		}
		if l["event"] == "verified" && l["file"] == "model.safetensors" {
			t.Error("verified emitted for the bad file")
		}
	}
	st := mustStatus(t, root)
	if len(st.Models) != 0 || len(st.Incomplete) != 1 || st.Incomplete[0].ModelID != testID {
		t.Errorf("status = %+v", st)
	}
}

func mustStatus(t *testing.T, root string) StatusReport {
	st, err := Status(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInstallResumesAfterInterruption(t *testing.T) {
	f := newFakeHF(t)
	in, rec, dir := newInstaller(t, f)
	var drop int32 = 1
	f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
		if name == "model.safetensors" && atomic.CompareAndSwapInt32(&drop, 1, 0) {
			w.Header().Set("Content-Length", fmt.Sprint(len(d)))
			w.WriteHeader(200)
			w.Write(d[:200000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		return false
	}
	if _, err := in.Install(context.Background(), testID); err == nil {
		t.Fatal("interrupted install succeeded")
	}
	root, _ := filepath.EvalSymlinks(dir)
	if st, err := os.Stat(filepath.Join(root, "models", "."+testID+".partial", "model.safetensors.part")); err != nil || st.Size() != 200000 {
		t.Fatalf("part not kept: %v", err)
	}
	rec.events = nil
	rc, err := in.Install(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeVerify(t, rc.Dir, rc.ManifestSHA256)
	resumed := false
	for _, r := range f.requests() {
		resumed = resumed || r.Range == "bytes=200000-"
	}
	if !resumed {
		t.Error("the second install did not resume with a Range request")
	}
	lines := rec.lines(t)
	if lines[0]["event"] != "start" || lines[len(lines)-1]["event"] != "done" {
		t.Errorf("events = %v", lines)
	}
	if _, err := os.Stat(filepath.Join(root, "models", "."+testID+".partial")); err == nil {
		t.Error("staging dir remains after success")
	}
}

func TestInstallCleansStrayStagingFiles(t *testing.T) {
	f := newFakeHF(t)
	in, _, dir := newInstaller(t, f)
	root, _ := prepareRoot(dir)
	stage := filepath.Join(root, "models", "."+testID+".partial")
	os.MkdirAll(filepath.Join(stage, "sub"), 0o700)
	os.WriteFile(filepath.Join(stage, "evil.py"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(stage, "old-revision.safetensors"), []byte("x"), 0o600)
	os.Symlink("/etc/passwd", filepath.Join(stage, "config.json")) // a symlink squatting a declared name
	rc, err := in.Install(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeVerify(t, rc.Dir, rc.ManifestSHA256)
}

func TestInstallCancel(t *testing.T) {
	f := newFakeHF(t)
	in, rec, _ := newInstaller(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
		if name != "model.safetensors" {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(d)))
		w.WriteHeader(200)
		w.Write(d[:100000])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return true
	}
	in.Emit = func(ev any) {
		rec.emit(ev)
		if p, ok := ev.(ProgressEvent); ok && p.File == "model.safetensors" && p.Bytes >= 100000 {
			cancel()
		}
	}
	_, err := in.Install(ctx, testID)
	wantCode(t, err, "cancelled")
	lines := rec.lines(t)
	if last := lines[len(lines)-1]; last["event"] != "error" || last["code"] != "cancelled" {
		t.Errorf("last = %v", last)
	}
	if st := mustStatus(t, in.InferenceDir); len(st.Models) != 0 || len(st.Incomplete) != 1 {
		t.Errorf("status = %+v", st)
	}
}

func TestInstallRefusesExistingDirWithoutReceipt(t *testing.T) {
	f := newFakeHF(t)
	in, _, dir := newInstaller(t, f)
	root, _ := prepareRoot(dir)
	os.MkdirAll(filepath.Join(root, "models", testID), 0o700)
	_, err := in.Install(context.Background(), testID)
	wantCode(t, err, "model_dir_exists")
}

func TestInstallLockExcludesSecondInstaller(t *testing.T) {
	root, _ := prepareRoot(filepath.Join(t.TempDir(), "inference"))
	unlock, err := lockInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockInstall(root); err == nil {
		t.Error("second lock acquired")
	}
	unlock()
	u2, err := lockInstall(root)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	u2()
}

func TestInstallFilesAreHashedNotTrustedByName(t *testing.T) {
	// A shard-named pin needs the index; the install of a sharded pin works end to end.
	f := newFakeHF(t)
	delete(f.files, "model.safetensors")
	f.files["model-00001-of-00002.safetensors"] = []byte(strings.Repeat("a", 3000))
	f.files["model-00002-of-00002.safetensors"] = []byte(strings.Repeat("b", 3000))
	f.files[weightIndexName] = jsonBytes(map[string]any{"weight_map": map[string]any{"a": "model-00001-of-00002.safetensors", "b": "model-00002-of-00002.safetensors"}})
	in, _, _ := newInstaller(t, f)
	rc, err := in.Install(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeVerify(t, rc.Dir, rc.ManifestSHA256)
}

func TestManifestMemoryNeverUnderstatesWeights(t *testing.T) {
	f := newFakeHF(t)
	c := f.pinnedCatalog(t)
	var e CatalogEntry
	for _, x := range c.Entries {
		if x.ID == testID {
			e = x
		}
	}
	e.Files = append([]PinFile(nil), e.Files...)
	for i := range e.Files {
		if e.Files[i].Name == "model.safetensors" {
			e.Files[i].Size = e.Estimate.WeightsBytes + 12345
		}
	}
	m, warns, err := BuildManifest(e)
	if err != nil {
		t.Fatal(err)
	}
	if m.Memory.WeightsBytes != e.Estimate.WeightsBytes+12345 || m.Memory.LoadPeakBytes < m.Memory.WeightsBytes || len(warns) != 1 {
		t.Errorf("memory = %+v warns=%v", m.Memory, warns)
	}
	if _, _, err := BuildManifest(baseCatalog(t).Entries[0]); err == nil {
		t.Error("manifest built for an unpinned entry")
	}
	b1, _ := m.Bytes()
	b2, _ := m.Bytes()
	if string(b1) != string(b2) || !strings.HasSuffix(string(b1), "}\n") {
		t.Error("manifest encoding not deterministic")
	}
}
