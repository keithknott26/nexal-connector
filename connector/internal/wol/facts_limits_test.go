package wol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"
)

func TestCollectLocalInterfaceErrorReportsEmptyFacts(t *testing.T) {
	withFakes(t, nil, nil, &fakeUDP{})
	systemInterfaces = func() ([]ifaceV4, []net.Interface, error) { return nil, nil, errors.New("boom") }
	f := CollectLocal(context.Background())
	if f.MACs == nil || len(f.MACs) != 0 || f.Prefixes == nil || len(f.Prefixes) != 0 {
		t.Fatalf("MACs=%#v Prefixes=%#v; want empty non-nil slices", f.MACs, f.Prefixes)
	}
	if len(f.Addresses) != 0 || f.LANKey != "" {
		t.Fatalf("Addresses=%v LANKey=%q; want none", f.Addresses, f.LANKey)
	}
	if f.WakeForNetwork == "" {
		t.Fatal("WakeForNetwork must never be empty")
	}
}

func TestCollectLocalCapsMACsAndAddresses(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	var raw []net.Interface
	var v4 []ifaceV4
	for i := 0; i < MaxMACs+4; i++ {
		name := fmt.Sprintf("en%d", i)
		raw = append(raw, net.Interface{Name: name, Flags: up, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 1, 2, byte(i + 1)}})
		v4 = append(v4, ifaceV4{Name: name, Flags: up, Prefix: netip.MustParsePrefix(fmt.Sprintf("192.168.%d.5/24", i+1))})
	}
	withFakes(t, v4, raw, &fakeUDP{})
	f := CollectLocal(context.Background())
	if len(f.MACs) != MaxMACs {
		t.Fatalf("len(MACs) = %d, want cap %d", len(f.MACs), MaxMACs)
	}
	if f.MACs[0] != "3c:22:fb:01:02:01" || f.MACs[MaxMACs-1] != fmt.Sprintf("3c:22:fb:01:02:%02x", MaxMACs) {
		t.Fatalf("cap must keep the lowest sorted MACs, got %v", f.MACs)
	}
	if len(f.Addresses) != MaxLANAddresses {
		t.Fatalf("len(Addresses) = %d, want cap %d", len(f.Addresses), MaxLANAddresses)
	}
	if len(f.Prefixes) != MaxMACs+4 {
		t.Fatalf("Prefixes = %d, want all %d (only MACs/addresses are capped)", len(f.Prefixes), MaxMACs+4)
	}
}
