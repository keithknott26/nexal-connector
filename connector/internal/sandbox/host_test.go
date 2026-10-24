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

func TestParseDFFreeMalformed(t *testing.T) {
	for name, out := range map[string]string{
		"empty":          "",
		"header only":    "Filesystem 1024-blocks Used Available Capacity Mounted on\n",
		"too few fields": "Filesystem 1024-blocks Used Available\n/dev/disk3s5 1 2\n",
		"non-numeric":    "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s5 1 2 lots 5% /\n",
		"negative":       "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s5 1 2 -4 5% /\n",
	} {
		if _, err := ParseDFFree(out); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// Only the last line is read, and trailing blank lines are tolerated.
	got, err := ParseDFFree("hdr\n\n/dev/x 10 5 7 40% /\n\n")
	if err != nil || got != 7<<10 {
		t.Fatalf("got %d err %v", got, err)
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
