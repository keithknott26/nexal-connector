package inference

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installed(t *testing.T) (root string, rc Receipt) {
	f := newFakeHF(t)
	in, _, dir := newInstaller(t, f)
	rc, err := in.Install(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(dir)
	return root, rc
}

func statusOf(t *testing.T, root, name string, chk VerifyResult, want string) {
	t.Helper()
	for _, f := range chk.Files {
		if f.Name == name {
			if f.Status != want {
				t.Errorf("%s = %s, want %s", name, f.Status, want)
			}
			return
		}
	}
	t.Errorf("%s not reported", name)
}

func TestStatusAndVerifyHealthy(t *testing.T) {
	root, rc := installed(t)
	st := mustStatus(t, root)
	if len(st.Models) != 1 {
		t.Fatalf("status = %+v", st)
	}
	m := st.Models[0]
	var onDisk int64
	for _, f := range rc.Files {
		onDisk += f.Size
	}
	onDisk += rc.ManifestBytes
	if m.ModelID != testID || m.State != StateInstalledUnmeasured || m.Revision != testRev || m.BytesOnDisk != onDisk || m.InstalledAt != rc.InstalledAt ||
		!m.EstimatedMemory || len(m.Problems) != 0 || m.Dir != rc.Dir || m.ManifestSHA256 != rc.ManifestSHA256 {
		t.Errorf("model = %+v", m)
	}
	for _, full := range []bool{false, true} {
		v, err := CheckModel(context.Background(), root, testID, full)
		if err != nil || !v.OK || v.State != StateInstalledUnmeasured || len(v.Files) != 5 || v.Full != full {
			t.Errorf("verify(full=%v) = %+v %v", full, v, err)
		}
	}
	empty := mustStatus(t, t.TempDir())
	if empty.Models == nil || empty.Incomplete == nil || len(empty.Models) != 0 {
		t.Errorf("empty status must have empty arrays: %+v", empty)
	}
}

func TestVerifyDetectsTamperingAndStatusSeesSizeChanges(t *testing.T) {
	root, rc := installed(t)
	model := rc.Dir
	// Same-size tamper: cheap status misses it (by design); full verify catches it.
	p := filepath.Join(model, "config.json")
	os.Chmod(p, 0o600)
	b, _ := os.ReadFile(p)
	b[10] ^= 0x01
	os.WriteFile(p, b, 0o600)
	os.Chmod(p, 0o400)
	if st := mustStatus(t, root); st.Models[0].State != StateInstalledUnmeasured {
		t.Errorf("cheap status flagged a same-size change (it cannot know): %+v", st.Models[0])
	}
	v, _ := CheckModel(context.Background(), root, testID, true)
	if v.OK || v.State != StateDamaged {
		t.Fatalf("verify = %+v", v)
	}
	statusOf(t, root, "config.json", v, "hash_mismatch")

	// Size change: status sees it.
	q := filepath.Join(model, "tokenizer.json")
	os.Chmod(q, 0o600)
	os.WriteFile(q, []byte("short"), 0o400)
	if st := mustStatus(t, root); st.Models[0].State != StateDamaged || len(st.Models[0].Problems) == 0 {
		t.Errorf("status = %+v", st.Models[0])
	}
	v, _ = CheckModel(context.Background(), root, testID, false)
	statusOf(t, root, "tokenizer.json", v, "size_mismatch")

	// Missing, unexpected, symlinked, writable.
	os.Chmod(model, 0o700)
	os.Remove(filepath.Join(model, "tokenizer_config.json"))
	os.WriteFile(filepath.Join(model, "evil.py"), []byte("x"), 0o600)
	os.Remove(filepath.Join(model, "model.safetensors"))
	os.Symlink("/etc/hosts", filepath.Join(model, "model.safetensors"))
	os.Chmod(filepath.Join(model, ManifestFileName), 0o666)
	v, _ = CheckModel(context.Background(), root, testID, true)
	statusOf(t, root, "tokenizer_config.json", v, "missing")
	statusOf(t, root, "evil.py", v, "unexpected")
	statusOf(t, root, "model.safetensors", v, "not_regular")
	statusOf(t, root, ManifestFileName, v, "bad_mode")
	if v.OK {
		t.Error("ok despite tampering")
	}
	// Directory gone entirely.
	os.RemoveAll(model)
	v, _ = CheckModel(context.Background(), root, testID, true)
	if v.OK || v.State != StateDamaged || len(v.Problems) == 0 {
		t.Errorf("verify = %+v", v)
	}
	// Not installed at all.
	_, err := CheckModel(context.Background(), root, "qwen3-8b-q4", true)
	wantCode(t, err, "not_installed")
	_, err = CheckModel(context.Background(), root, "../x", true)
	wantCode(t, err, "invalid_arguments")
}

func TestVerifyCancel(t *testing.T) {
	root, _ := installed(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CheckModel(ctx, root, testID, true)
	wantCode(t, err, "cancelled")
}

func TestReceiptStrictness(t *testing.T) {
	root, _ := installed(t)
	p := receiptPath(root, testID)
	good := mustRead(t, p)
	for name, mut := range map[string]string{
		"unknown field": strings.Replace(string(good), `"state"`, `"extra": 1, "state"`, 1),
		"duplicate key": strings.Replace(string(good), `"state"`, `"state": "x", "state"`, 1),
		"wrong id":      strings.Replace(string(good), `"modelId": "`+testID+`"`, `"modelId": "qwen3-8b-q4"`, 1),
		"wrong schema":  strings.Replace(string(good), `"schemaVersion": 1`, `"schemaVersion": 9`, 1),
		"bad file name": strings.Replace(string(good), `"config.json"`, `"../config.json"`, 1),
	} {
		os.WriteFile(p, []byte(mut), 0o600)
		_, err := LoadReceipt(root, testID)
		wantCode(t, err, "receipt_invalid")
		if st := mustStatus(t, root); len(st.Models) != 1 || st.Models[0].State != StateDamaged {
			t.Errorf("%s: status = %+v", name, st.Models)
		}
	}
}

func TestRemove(t *testing.T) {
	root, rc := installed(t)
	_, err := Remove(root, testID, false)
	wantCode(t, err, "confirmation_required")
	if _, e := os.Stat(rc.Dir); e != nil {
		t.Fatal("removed without --yes")
	}
	// Leave a partial download too.
	os.MkdirAll(stagingPath(root, testID), 0o700)
	res, err := Remove(root, testID, true)
	if err != nil || len(res.Removed) != 3 {
		t.Fatalf("remove = %+v %v", res, err)
	}
	for _, p := range []string{rc.Dir, stagingPath(root, testID), receiptPath(root, testID)} {
		if _, e := os.Lstat(p); e == nil {
			t.Errorf("%s remains", p)
		}
	}
	_, err = Remove(root, testID, true)
	wantCode(t, err, "not_installed")
}

func TestRemoveRefusesEscapes(t *testing.T) {
	root, _ := installed(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("keep"), 0o600)
	for _, id := range []string{"../models", "..", ".", "../../" + filepath.Base(outside), outside, "a/b", "", "UPPER", "x\x00y", "-rf"} {
		_, err := Remove(root, id, true)
		wantCode(t, err, "invalid_arguments")
	}
	if _, err := Remove("relative/dir", testID, true); err == nil {
		t.Error("relative inference dir accepted")
	}
	// A symlinked model directory is never followed or deleted through.
	link := filepath.Join(root, "models", "evil-1b")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := Remove(root, "evil-1b", true)
	wantCode(t, err, "path_escape")
	if _, e := os.Stat(filepath.Join(outside, "precious.txt")); e != nil {
		t.Fatal("followed a symlink out of the models root")
	}
	// A symlinked staging directory too.
	os.Remove(link)
	os.Symlink(outside, stagingPath(root, "evil-1b"))
	_, err = Remove(root, "evil-1b", true)
	wantCode(t, err, "path_escape")
	if _, e := os.Stat(filepath.Join(outside, "precious.txt")); e != nil {
		t.Fatal("followed a staging symlink")
	}
	// A symlinked models root.
	root2 := t.TempDir()
	os.Symlink(outside, filepath.Join(root2, "models"))
	_, err = Remove(root2, testID, true)
	wantCode(t, err, "not_installed")
	if _, e := os.Stat(filepath.Join(outside, "precious.txt")); e != nil {
		t.Fatal("followed a models-root symlink")
	}
	// A receipt that is a symlink is not removed through.
	os.Symlink(filepath.Join(outside, "precious.txt"), receiptPath(root, "evil-1b"))
	os.MkdirAll(filepath.Join(root, "models", "evil-1b"), 0o700)
	_, err = Remove(root, "evil-1b", true)
	wantCode(t, err, "path_escape")
	if _, e := os.Stat(filepath.Join(outside, "precious.txt")); e != nil {
		t.Fatal("deleted through a receipt symlink")
	}
}
