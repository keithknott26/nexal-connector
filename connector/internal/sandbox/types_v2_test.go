package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskUnmarshalWireShape(t *testing.T) {
	var task Task
	body := `{"id":"sbt_9","kind":"create","sandboxId":"sb9","hostname":"sbx-9","desktop":true,
	  "image":{"url":"https://x","sha256":"` + strings.Repeat("b", 64) + `","arch":"aarch64","cloudInitFlavor":"nocloud"},
	  "resources":{"cpu":4,"memoryMb":4096,"diskGb":20},
	  "mesh":{"managementUrl":"https://m.example","setupKey":"K"},
	  "sshAuthorizedKeys":["ssh-ed25519 AAAA x"],"vncPassword":null,
	  "persistent":true,"sandboxKind":"devcontainer","devcontainer":{"template":"go"},"unknown":1}`
	if err := json.Unmarshal([]byte(body), &task); err != nil {
		t.Fatal(err)
	}
	if task.TaskID != "sbt_9" || task.Size != (Size{CPUs: 4, MemoryMB: 4096, DiskGB: 20}) || task.SetupKey != "K" ||
		task.ManagementURL != "https://m.example" || !task.Image.CloudInit || !task.IsPersistent() || !task.IsDev() ||
		task.VNCPassword != "" {
		t.Fatalf("%+v", task)
	}
}

func TestIsDevInference(t *testing.T) {
	if (Task{}).IsDev() {
		t.Fatal("empty task is a VM")
	}
	if !(Task{Devcontainer: &Devcontainer{Template: "go"}}).IsDev() {
		t.Fatal("devcontainer payload implies dev")
	}
	if (Task{SandboxKind: SandboxVM, Devcontainer: &Devcontainer{Template: "go"}}).IsDev() {
		t.Fatal("explicit vm wins")
	}
}

func TestValidateDevcontainer(t *testing.T) {
	ok := []Devcontainer{{RepoURL: "https://github.com/a/b"}, {Template: "node"}, {JSON: `{"image":"x"}`}}
	for _, d := range ok {
		d := d
		if err := ValidateDevcontainer(&d); err != nil {
			t.Errorf("%+v: %v", d, err)
		}
	}
	bad := []*Devcontainer{nil, {}, {RepoURL: "http://x/y"}, {RepoURL: "https://x/y z"}, {Template: "../etc"},
		{JSON: "{"}, {Template: "go", JSON: "{}"}, {RepoURL: "https://x/$(id)"}}
	for i, d := range bad {
		if ValidateDevcontainer(d) == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
}

func TestValidateBootV2(t *testing.T) {
	base := Task{TaskID: "t", SandboxID: "s", Kind: KindCreate, Hostname: "h1", SetupKey: "K",
		Size: Size{CPUs: 1, MemoryMB: 512, DiskGB: 5}}
	if err := ValidateBoot(base); err != nil {
		t.Fatal(err)
	}
	dev := base
	dev.Size = Size{}
	dev.SandboxKind = SandboxDevcontainer
	dev.Devcontainer = &Devcontainer{Template: "go"}
	if err := ValidateBoot(dev); err != nil {
		t.Fatalf("dev without resources: %v", err)
	}
	bad := []func(*Task){
		func(x *Task) { x.Lifecycle = "forever" },
		func(x *Task) { x.DriveMode = "rwx" },
		func(x *Task) { x.DriveToken = "a b" },
		func(x *Task) { x.SSHCAPublicKey = "ssh-ed25519 AAA$(id)" },
		func(x *Task) { x.ManagementURL = "http://plain" },
		func(x *Task) { x.SandboxKind = "box" },
	}
	for i, mut := range bad {
		x := base
		mut(&x)
		if ValidateBoot(x) == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
	vm := Task{TaskID: "t", SandboxID: "s", Kind: KindVNCPassword}
	if ValidateTask(vm) != nil {
		t.Fatal("vnc-password is a valid task kind")
	}
}

func TestStatePausedTransitions(t *testing.T) {
	for _, c := range [][2]State{{StateRunning, StatePaused}, {StatePaused, StateRunning}, {StatePaused, StateStopping},
		{StateProvisioning, StateProvisioning}} {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%q -> %q should be allowed", c[0], c[1])
		}
	}
	if !StatePaused.Active() {
		t.Fatal("paused sandboxes still hold resources")
	}
}

func TestTaskUnmarshalDigest(t *testing.T) {
	var task Task
	d := "sha512:" + strings.Repeat("e", 128)
	body := `{"id":"sbt_1","kind":"create","sandboxId":"sb1","image":{"url":"https://x/u.qcow2","digest":"` + d + `","arch":"arm64","cloudInitFlavor":"nocloud"}}`
	if err := json.Unmarshal([]byte(body), &task); err != nil {
		t.Fatal(err)
	}
	got, err := task.Image.Digest()
	if err != nil || got.String() != d || task.Image.SHA256 != "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestValidateTaskAcceptsRemoteStopAndStart(t *testing.T) {
	for _, k := range []Kind{KindStop, KindStart} {
		if err := ValidateTask(Task{TaskID: "t1", SandboxID: "s1", Kind: k}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if err := ValidateTask(Task{TaskID: "t1", SandboxID: "s1", Kind: "pause"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
