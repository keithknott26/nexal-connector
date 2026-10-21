package timemachine

import (
	"errors"
	"fmt"
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
	if got := c.DestinationURL(); got != "smb://tm33333333333343338333@tm-gw-1.netbird.cloud/tm33333333333343338333" || got != c.RedactedURL() {
		t.Fatal(got)
	}
	if s := fmt.Sprintf("%v %+v %#v %s", c, c, c, c); strings.Contains(s, c.Password) {
		t.Fatal("password leaked: " + s)
	}
	odd := SMBCredential{Destination: Destination{Username: "a@b c:d", Host: "100.92.3.4", Share: "s h/x"}}
	if got := odd.DestinationURL(); got != "smb://a%40b%20c%3Ad@100.92.3.4/s%20h/x" {
		t.Fatal(got)
	}
	withURL := c
	withURL.URL = "smb://u:" + c.Password + "@h/s"
	withURL.Zero()
	if withURL.Password != "" || withURL.URL != "" {
		t.Fatal("Zero left secret fields")
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

func TestValidMeshHost(t *testing.T) {
	for host, want := range map[string]bool{
		"tm-gw-1.netbird.cloud":           true,
		"100.64.0.1":                      true,
		"100.127.255.254":                 true,
		"100.128.0.1":                     false,
		"100.63.255.255":                  false,
		"8.8.8.8":                         false,
		"fd00::1":                         false,
		"localhost":                       false,
		"Upper.Example.com":               false,
		"-bad.example.com":                false,
		"":                                false,
		strings.Repeat("a", 250) + ".com": false,
	} {
		if got := ValidMeshHost(host); got != want {
			t.Errorf("ValidMeshHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDestinationValidate(t *testing.T) {
	if err := dest().Validate(); err != nil {
		t.Fatalf("valid destination rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Destination){
		"protocol":          func(d *Destination) { d.Protocol = "afp" },
		"port":              func(d *Destination) { d.Port = 139 },
		"public host":       func(d *Destination) { d.Host = "8.8.8.8" },
		"share pattern":     func(d *Destination) { d.Share = "../etc"; d.Username = d.Share },
		"username mismatch": func(d *Destination) { d.Username = "tm00000000000000000000" },
	} {
		d := dest()
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestZeroDropsSecrets(t *testing.T) {
	c := &SMBCredential{Destination: dest(), Password: "secret", URL: "smb://u:secret@h/s"}
	c.Zero()
	if c.Password != "" || c.URL != "" || c.Destination != dest() {
		t.Fatalf("SMBCredential.Zero wrong result: %+v", c)
	}
	s := &Credentials{AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c", Bucket: "bk"}
	s.Zero()
	if s.AccessKeyID != "" || s.SecretAccessKey != "" || s.SessionToken != "" || s.Bucket != "bk" {
		t.Fatalf("Credentials.Zero wrong result: %+v", s)
	}
	(*SMBCredential)(nil).Zero()
	(*Credentials)(nil).Zero()
}
