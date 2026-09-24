package timemachine

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{Enabled: true, Entitled: true, Revision: 1, ShareName: "NexalBackup", MountPath: "/mnt/nexal-backup", Backend: BackendJuiceFS, QuotaBytes: 500 << 30, MeshCIDRs: []string{"100.64.0.0/10", "10.20.0.0/16"}, Advertise: true}
}

func TestDisabledIsClosedAndNeedsNoConfig(t *testing.T) {
	c := Config{}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := Evaluate(c, Observation{}); got.State != "disabled" || got.Advertised {
		t.Fatalf("%+v", got)
	}
}

func TestValidationRejectsEntitlementSecretsAndPublicNetworks(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"not entitled": func(c *Config) { c.Entitled = false }, "root": func(c *Config) { c.MountPath = "/" },
		"too small": func(c *Config) { c.QuotaBytes = 1 }, "public cidr": func(c *Config) { c.MeshCIDRs = []string{"8.8.8.0/24"} },
		"noncanonical cidr": func(c *Config) { c.MeshCIDRs = []string{"10.0.0.1/8"} }, "unsafe name": func(c *Config) { c.ShareName = "x]\nguest ok = yes" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestStatusRequiresObservedBackendSMBAndAdvertisement(t *testing.T) {
	c := validConfig()
	o := Observation{Platform: "linux", AdminAvailable: true, FreeBytes: 600 << 30}
	if got := Evaluate(c, o); got.DetailCode != "juicefs_mount_required" {
		t.Fatalf("%+v", got)
	}
	o.BackendMounted = true
	o.BackendType = BackendJuiceFS
	if got := Evaluate(c, o); got.DetailCode != "smb_configuration_pending" {
		t.Fatalf("%+v", got)
	}
	o.SMBConfigured = true
	o.SMBHealthy = true
	if got := Evaluate(c, o); got.DetailCode != "bonjour_advertisement_missing" {
		t.Fatalf("%+v", got)
	}
	o.Advertised = true
	if got := Evaluate(c, o); got.State != "ready" {
		t.Fatalf("%+v", got)
	}
}

func TestSambaStanzaIsFruitCapableQuotaBoundAndMeshOnly(t *testing.T) {
	b, err := RenderSambaShare(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vfs objects = catia fruit streams_xattr", "fruit:time machine = yes", "fruit:time machine max size = 536870912000", "smb encrypt = required", "server signing = mandatory", "hosts allow = 100.64.0.0/10 10.20.0.0/16", "hosts deny = 0.0.0.0/0 ::/0", "guest ok = no"} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %q", want)
		}
	}
}
