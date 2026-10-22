package timemachine

import (
	"strings"
	"testing"
)

func TestRenderSambaShareHardensAndInterpolatesConfig(t *testing.T) {
	out, err := RenderSambaShare(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[NexalBackup]\n",
		"path = /mnt/nexal-backup\n",
		"guest ok = no\n",
		"hosts allow = 100.64.0.0/10 10.20.0.0/16\n",
		"hosts deny = 0.0.0.0/0 ::/0\n",
		"smb encrypt = required\n",
		"server signing = mandatory\n",
		"fruit:time machine = yes\n",
		"fruit:time machine max size = 536870912000\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("share stanza missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSambaShareRefusesDisabledAndInvalidConfig(t *testing.T) {
	if _, err := RenderSambaShare(Config{}); err == nil {
		t.Fatal("disabled config must not render a share")
	}
	c := validConfig()
	c.MeshCIDRs = nil
	if _, err := RenderSambaShare(c); err == nil {
		t.Fatal("config without mesh CIDRs must not render a share")
	}
}
