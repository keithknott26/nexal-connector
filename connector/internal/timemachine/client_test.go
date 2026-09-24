package timemachine

import (
	"errors"
	"strings"
	"testing"
)

func dest() Destination {
	return Destination{Protocol: "smb", Host: "tm-gw-1.netbird.cloud", Port: 445, Share: "tm33333333333343338333", Username: "tm33333333333343338333"}
}

func TestClientConfigValidation(t *testing.T) {
	d := dest()
	ok := ClientConfig{Enabled: true, Role: RoleClient, Destination: &d, CredentialEndpoint: "/api/v2/devices/h/time-machine/credentials"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ClientConfig){
		"server role":    func(c *ClientConfig) { c.Role = "server" },
		"public ip":      func(c *ClientConfig) { x := dest(); x.Host = "51.81.1.1"; c.Destination = &x },
		"bad account":    func(c *ClientConfig) { x := dest(); x.Share = "../etc"; c.Destination = &x },
		"creds included": func(c *ClientConfig) { c.CredentialsIncluded = true },
		"missing target": func(c *ClientConfig) { c.Destination = nil },
		"user mismatch":  func(c *ClientConfig) { x := dest(); x.Username = "tm00000000000000000000"; c.Destination = &x },
	} {
		c := ok
		mutate(&c)
		if c.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	mesh := dest()
	mesh.Host = "100.92.3.4"
	if mesh.Validate() != nil {
		t.Fatal("mesh literal refused")
	}
}

func TestSMBCredentialURLAndRedaction(t *testing.T) {
	c := SMBCredential{Destination: dest(), Password: "abcdefghijklmnopqrstuvwx-_12"}
	if err := c.Validate(dest()); err != nil {
		t.Fatal(err)
	}
	if got := c.TmutilURL(); got != "smb://tm33333333333343338333:abcdefghijklmnopqrstuvwx-_12@tm-gw-1.netbird.cloud/tm33333333333343338333" {
		t.Fatal(got)
	}
	if strings.Contains(c.RedactedURL(), c.Password) || strings.Contains(c.String(), c.Password) {
		t.Fatal("password leaked")
	}
	other := dest()
	other.Host = "evil.example.com"
	if c.Validate(other) == nil {
		t.Fatal("destination mismatch accepted")
	}
	c.Password = "short"
	if c.Validate(dest()) == nil {
		t.Fatal("weak password accepted")
	}
}

func TestResolvesInsideMesh(t *testing.T) {
	if err := ResolvesInsideMesh("gw", func(string) ([]string, error) { return []string{"100.100.1.2"}, nil }); err != nil {
		t.Fatal(err)
	}
	if ResolvesInsideMesh("gw", func(string) ([]string, error) { return []string{"100.100.1.2", "51.81.1.1"}, nil }) == nil {
		t.Fatal("public resolution accepted")
	}
	if err := ResolvesInsideMesh("gw", func(string) ([]string, error) { return nil, errors.New("nx") }); err == nil || !strings.HasPrefix(err.Error(), "mesh_not_connected") {
		t.Fatal("unresolvable accepted")
	}
}
