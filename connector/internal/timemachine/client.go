package timemachine

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Gateway-client mode: this Mac backs up to an operator-run storage gateway
// reached directly over the private mesh (WireGuard). Nothing is served from
// this Mac and no object-storage credential ever reaches it.

const RoleClient = "client"

type Destination struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Share    string `json:"share"`
	Username string `json:"username"`
}

type ClientConfig struct {
	Enabled             bool         `json:"enabled"`
	Role                string       `json:"role"`
	ServiceState        string       `json:"serviceState"`
	ComputerID          string       `json:"computerId"`
	NetworkID           string       `json:"networkId"`
	Destination         *Destination `json:"destination"`
	CredentialEndpoint  string       `json:"credentialEndpoint"`
	CredentialsIncluded bool         `json:"credentialsIncluded"`
	QuotaBytes          uint64       `json:"quotaBytes"`
	RefreshAfterSeconds uint64       `json:"refreshAfterSeconds"`
}

var (
	accountPattern  = regexp.MustCompile(`^tm[a-f0-9]{20}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24,128}$`)
	hostPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// ValidMeshHost accepts a mesh DNS name or an RFC 6598 (100.64.0.0/10) mesh
// address. A public literal address is refused so SMB never leaves the mesh.
func ValidMeshHost(host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(a)
	}
	return len(host) <= 253 && hostPattern.MatchString(host)
}

func (d Destination) Validate() error {
	if d.Protocol != "smb" || d.Port != 445 || !ValidMeshHost(d.Host) || !accountPattern.MatchString(d.Share) || d.Username != d.Share {
		return errors.New("invalid Time Machine gateway destination")
	}
	return nil
}

func (c ClientConfig) Validate() error {
	if c.Role != RoleClient {
		return errors.New("not a gateway-client Time Machine configuration")
	}
	if !c.Enabled {
		return nil
	}
	if c.Destination == nil || c.CredentialsIncluded || !strings.HasSuffix(c.CredentialEndpoint, "/time-machine/credentials") {
		return errors.New("invalid Time Machine client configuration")
	}
	return c.Destination.Validate()
}

// SMBCredential is released only to an enrolled computer of an enabled network.
type SMBCredential struct {
	Destination
	Password string `json:"password"`
	URL      string `json:"url"`
}

func (SMBCredential) String() string { return "time-machine-smb-credential:[redacted]" }

func (c SMBCredential) Validate(expected Destination) error {
	if err := c.Destination.Validate(); err != nil {
		return err
	}
	if c.Destination != expected {
		return errors.New("Time Machine credential does not match the configured destination")
	}
	if !passwordPattern.MatchString(c.Password) {
		return errors.New("invalid Time Machine credential")
	}
	return nil
}

// TmutilURL is the argument for `tmutil setdestination -a`. It contains the
// password and must never be logged.
func (c SMBCredential) TmutilURL() string {
	u := url.URL{Scheme: "smb", User: url.UserPassword(c.Username, c.Password), Host: c.Host, Path: "/" + c.Share}
	return u.String()
}

// RedactedURL is safe to print.
func (c SMBCredential) RedactedURL() string {
	u := url.URL{Scheme: "smb", User: url.User(c.Username), Host: c.Host, Path: "/" + c.Share}
	return u.String()
}

func (c *SMBCredential) Zero() {
	if c != nil {
		c.Password = ""
	}
}

// ResolvesInsideMesh checks that the gateway name resolves only to mesh
// addresses, i.e. the mesh (and its DNS) is up on this Mac. Otherwise Time
// Machine would silently try the public internet or fail later with a vague error.
func ResolvesInsideMesh(host string, lookup func(string) ([]string, error)) error {
	if a, err := netip.ParseAddr(host); err == nil {
		if !ValidMeshHost(a.String()) {
			return errors.New("gateway address is not a mesh address")
		}
		return nil
	}
	if lookup == nil {
		lookup = net.LookupHost
	}
	addrs, err := lookup(host)
	if err != nil || len(addrs) == 0 {
		return errors.New("mesh_not_connected: the storage gateway name does not resolve; connect the secure network first")
	}
	for _, raw := range addrs {
		a, err := netip.ParseAddr(raw)
		if err != nil || !a.Is4() || !netip.MustParsePrefix("100.64.0.0/10").Contains(a) {
			return errors.New("gateway name resolved outside the private mesh; refusing to send backups there")
		}
	}
	return nil
}
