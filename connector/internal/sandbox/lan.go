package sandbox

import (
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
)

// LAN bridging.
//
// VMs normally sit behind Virtualization.framework's NAT: reachable from this
// Mac and over the mesh, invisible on the home network. With the nexal-vmnet
// helper installed (macos/vmnet, a root launchd daemon in the mould of
// socket_vmnet), nexal-vmhost adds a second NIC bridged onto the Mac's active
// Wi-Fi/Ethernet, so the guest also gets an address from the home router.
// Bridging needs root (vmnet bridged mode) or Apple's restricted
// com.apple.vm.networking entitlement; the helper is the root route.

// LANBridgeSocket is where the nexal-vmnet helper listens (owned by the user it
// serves, mode 0600).
const LANBridgeSocket = "/var/run/nexal-vmnet.sock"

// lanBridgeAvailable reports whether the helper is installed and listening. It
// is replaced in tests.
var lanBridgeAvailable = func() bool {
	fi, err := os.Stat(LANBridgeSocket)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

var macPattern = regexp.MustCompile(`^02(:[0-9a-f]{2}){5}$`)

// guestNIC is one VM's network cards: the NAT NIC's address, and the LAN NIC's
// address and helper socket (both empty without a LAN NIC).
type guestNIC struct {
	NATMAC, LANMAC, Socket string
}

// guestNICs returns the NICs for a VM: without lan, none are pinned (nexal-vmhost
// keeps its own NAT address, as before). With lan, both MACs derive from the
// sandbox id -- stable across restarts, so the guest's network config (which
// matches them) and the router's DHCP lease survive -- locally administered
// unicast, "N"/"L" in the second octet so they never collide with each other.
func guestNICs(id string, lan bool) guestNIC {
	if !lan {
		return guestNIC{}
	}
	h := sha256.Sum256([]byte("nexal-guest-nic:" + id))
	mk := func(kind byte, b []byte) string {
		return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", kind, b[0], b[1], b[2], b[3])
	}
	return guestNIC{NATMAC: mk('N', h[0:4]), LANMAC: mk('L', h[4:8]), Socket: LANBridgeSocket}
}

// RenderNetworkConfig renders the NoCloud network-config for a two-NIC guest:
// nat0 carries the default route (the mesh and downloads keep using the Mac's
// NAT), lan0 is the home network with a worse route metric, and is optional so
// a guest whose bridge is down still boots without waiting for it.
func RenderNetworkConfig(p SeedParams) string {
	return "version: 2\n" +
		"ethernets:\n" +
		"  nat0:\n" +
		"    match:\n" +
		"      macaddress: \"" + p.NATMAC + "\"\n" +
		"    set-name: nat0\n" +
		"    dhcp4: true\n" +
		"    dhcp4-overrides:\n" +
		"      route-metric: 100\n" +
		"  lan0:\n" +
		"    match:\n" +
		"      macaddress: \"" + p.LANMAC + "\"\n" +
		"    set-name: lan0\n" +
		"    dhcp4: true\n" +
		"    dhcp4-overrides:\n" +
		"      route-metric: 200\n" +
		"    optional: true\n"
}
