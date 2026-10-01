package sandbox

import (
	"strings"
	"testing"
)

func TestParseDFFree(t *testing.T) {
	out := "Filesystem   1024-blocks      Used Available Capacity  Mounted on\n/dev/disk3s5  976490576 500000000 123456789    81%    /System/Volumes/Data\n"
	got, err := ParseDFFree(out)
	if err != nil || got != 123456789<<10 {
		t.Fatalf("got %d err %v", got, err)
	}
	if _, err := ParseDFFree("garbage"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseOnBattery(t *testing.T) {
	if !ParseOnBattery("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1) 80%; discharging") {
		t.Fatal("battery not detected")
	}
	if ParseOnBattery("Now drawing from 'AC Power'\n") {
		t.Fatal("AC misread as battery")
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	p := RenderLaunchdPlist("systems.nexal.vmhost.a", "/Applications/Nexal & Co/nexal-vmhost", "/tmp/a.json", "/tmp/a.log")
	for _, want := range []string{"<string>systems.nexal.vmhost.a</string>", "Nexal &amp; Co", "<string>--spec</string>", "<key>KeepAlive</key>\n\t<false/>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q\n%s", want, p)
		}
	}
}

func TestTaskRedaction(t *testing.T) {
	tk := Task{TaskID: "t1", SandboxID: "s1", Kind: KindCreate, SetupKey: "SECRETKEY", VNCPassword: "SECRETPW"}
	if s := tk.String(); strings.Contains(s, "SECRET") {
		t.Fatalf("String leaks: %s", s)
	}
}

func TestValidateTask(t *testing.T) {
	good := Task{TaskID: "t1", SandboxID: "s-1", Kind: KindDelete}
	if err := ValidateTask(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Task{
		{TaskID: "", SandboxID: "s", Kind: KindCreate},
		{TaskID: "t", SandboxID: "../etc", Kind: KindCreate},
		{TaskID: "t", SandboxID: "s", Kind: "explode"},
	} {
		if ValidateTask(bad) == nil {
			t.Errorf("expected rejection of %+v", bad)
		}
	}
}
