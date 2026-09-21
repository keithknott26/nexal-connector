package client

import (
	"context"
	"errors"
)

// The coordinator is the rendezvous server for WAN discovery. There is no
// broadcast on the internet, so "WAN discovery" is an authenticated list read
// from a control plane this host already holds a token for — no DHT, no gossip.
//
// What comes back is a candidate list. It is authorization only in the sense that
// the coordinator decided these peers belong to the same tenant and are approved,
// unrevoked and unpaused; the actual admission decision still happens per request
// in the mutual-TLS peer policy.

// PeerAddress is one dial candidate for a peer. Kind is "lan" or "wan". A wan
// address is server-observed: a host does not get to tell the coordinator its own
// public address, so a wan kind here was never asserted by the peer.
type PeerAddress struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

// PeerCapabilities is self-reported by the peer and unattested by anything. The
// field naming says so at every layer on purpose: these values may not alone
// unlock a gated path, and pool.PlanMLX already treats them as necessary rather
// than sufficient evidence.
type PeerCapabilities struct {
	ThunderboltGeneration int    `json:"thunderboltGeneration,omitempty"`
	RDMAEnabled           bool   `json:"rdmaEnabled,omitempty"`
	OSVersion             string `json:"osVersion,omitempty"`
	Chip                  string `json:"chip,omitempty"`
}

// DirectoryPeer is one same-tenant peer. A host without a device fingerprint is
// omitted by the coordinator, because a peer that cannot be cryptographically
// authenticated could only be dialed unauthenticated.
type DirectoryPeer struct {
	HostID                   string           `json:"hostId"`
	Name                     string           `json:"name"`
	Fingerprint              string           `json:"fingerprint"`
	Addresses                []PeerAddress    `json:"addresses"`
	SelfReportedCapabilities PeerCapabilities `json:"capabilities"`
}

// PeerDirectory carries the coordinator's two honesty fields verbatim. They are
// checked, not decoration: a response that does not say
// "candidate-list-not-authorization" is a response from something that does not
// share this contract, and it is refused rather than interpreted.
type PeerDirectory struct {
	Peers              []DirectoryPeer `json:"peers"`
	Authorization      string          `json:"authorization"`
	CapabilityEvidence string          `json:"capabilityEvidence"`
}

// AuthorizationCandidateList and CapabilityEvidenceSelfReported are the exact
// strings the API contract specifies.
const (
	AuthorizationCandidateList         = "candidate-list-not-authorization"
	CapabilityEvidenceSelfReported     = "self-reported-not-attested"
	maxDirectoryPeers                  = 128
	maxDirectoryAddressesPerPeer       = 9 // 8 lan + 1 wan, matching the server cap.
	errDirectorySchema                 = "invalid coordinator peer directory schema"
	errDirectoryUnauthorizedAssumption = "coordinator peer directory did not declare itself a candidate list"
)

// Peers reads the tenant's peer candidates. The tenant is never sent: it is read
// server-side from the host token's row.
func (c *Client) Peers(ctx context.Context) (PeerDirectory, error) {
	var out PeerDirectory
	if err := c.call(ctx, "GET", "/api/peers", nil, &out); err != nil {
		return PeerDirectory{}, err
	}
	if out.Authorization != AuthorizationCandidateList {
		return PeerDirectory{}, errors.New(errDirectoryUnauthorizedAssumption)
	}
	if out.CapabilityEvidence != CapabilityEvidenceSelfReported || len(out.Peers) > maxDirectoryPeers {
		return PeerDirectory{}, errors.New(errDirectorySchema)
	}
	for _, p := range out.Peers {
		if !ValidID(p.HostID) || len(p.Name) > 256 || len(p.Addresses) > maxDirectoryAddressesPerPeer {
			return PeerDirectory{}, errors.New(errDirectorySchema)
		}
		for _, a := range p.Addresses {
			if a.Kind != "lan" && a.Kind != "wan" {
				return PeerDirectory{}, errors.New(errDirectorySchema)
			}
		}
	}
	return out, nil
}
