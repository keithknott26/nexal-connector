package sandbox

import (
	"strings"
	"testing"
)

func TestValidateSizeBounds(t *testing.T) {
	ok := Size{CPUs: 1, MemoryMB: minMemoryMB, DiskGB: 1}
	if err := ValidateSize(ok); err != nil {
		t.Fatalf("minimum size rejected: %v", err)
	}
	max := Size{CPUs: maxCPUs, MemoryMB: maxMemoryMB, DiskGB: maxDiskGB}
	if err := ValidateSize(max); err != nil {
		t.Fatalf("maximum size rejected: %v", err)
	}
	cases := map[string]func(*Size){
		"cpus zero":      func(s *Size) { s.CPUs = 0 },
		"cpus too many":  func(s *Size) { s.CPUs = maxCPUs + 1 },
		"memory low":     func(s *Size) { s.MemoryMB = minMemoryMB - 1 },
		"memory high":    func(s *Size) { s.MemoryMB = maxMemoryMB + 1 },
		"disk zero":      func(s *Size) { s.DiskGB = 0 },
		"disk too large": func(s *Size) { s.DiskGB = maxDiskGB + 1 },
	}
	for name, mut := range cases {
		s := ok
		mut(&s)
		if ValidateSize(s) == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}

func TestValidateBootFields(t *testing.T) {
	base := Task{TaskID: "t", SandboxID: "s", Kind: KindCreate, Hostname: "h1", SetupKey: "K",
		Size: Size{CPUs: 1, MemoryMB: 512, DiskGB: 5}}
	good := base
	good.VNCPassword = "pw-1"
	good.SSHPublicKeys = []string{"ssh-ed25519 AAAA key"}
	if err := ValidateBoot(good); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Task){
		"zero size vm":    func(x *Task) { x.Size = Size{} },
		"bad hostname":    func(x *Task) { x.Hostname = "Bad_Host" },
		"bad setup key":   func(x *Task) { x.SetupKey = "has space" },
		"empty setup key": func(x *Task) { x.SetupKey = "" },
		"bad vnc":         func(x *Task) { x.VNCPassword = "a b" },
		"bad ssh key":     func(x *Task) { x.SSHPublicKeys = []string{"not-a-key"} },
		"newline ssh key": func(x *Task) { x.SSHPublicKeys = []string{"ssh-ed25519 AAAA\nx"} },
		"too many keys": func(x *Task) {
			x.SSHPublicKeys = make([]string, 65)
			for i := range x.SSHPublicKeys {
				x.SSHPublicKeys[i] = "ssh-ed25519 AAAA"
			}
		},
		"huge ssh key": func(x *Task) { x.SSHPublicKeys = []string{"ssh-ed25519 " + strings.Repeat("A", 8192)} },
	}
	for name, mut := range bad {
		x := base
		mut(&x)
		if ValidateBoot(x) == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}
