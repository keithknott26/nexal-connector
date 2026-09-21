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
	// ObservedAt is the coordinator's ISO-8601 timestamp. Declared because the
	// client decodes strictly; see the forward-compatibility note on PeerDirectory.
	ObservedAt string `json:"observedAt"`
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
	// LastSeenAt and CapabilitiesReportedAt are coordinator-assigned ISO-8601
	// timestamps. CapabilitiesReportedAt is null when a peer has never reported;
	// encoding/json leaves the zero value for null, which is the intended reading.
	LastSeenAt             string `json:"lastSeenAt"`
	CapabilitiesReportedAt string `json:"capabilitiesReportedAt"`
}

// PeerDirectory carries the coordinator's two honesty fields verbatim. They are
// checked, not decoration: a response that does not say
// "candidate-list-not-authorization" is a response from something that does not
// share this contract, and it is refused rather than interpreted.
// Every field the coordinator sends must be declared here, because c.call decodes
// with DisallowUnknownFields. That strictness is deliberate for config files and
// untrusted bundles, but it makes this struct a hard coupling to the coordinator's
// response: the Worker deploys instantly while installed connectors lag, so a new
// server field breaks every old client at once. See HARDENING-PLAN §43.
type PeerDirectory struct {
	Peers              []DirectoryPeer `json:"peers"`
	Authorization      string          `json:"authorization"`
	CapabilityEvidence string          `json:"capabilityEvidence"`
	// IdentityEvidence states that a fingerprint is enrollment-bound, not hardware
	// attested. Checked, not decorative, for the same reason as Authorization.
	IdentityEvidence string `json:"identityEvidence"`
	// PeerLimit and Truncated say whether the fleet exceeded the server page. A
	// truncated directory is not a complete view of the tenant and must never be
	// treated as one.
	PeerLimit int  `json:"peerLimit"`
	Truncated bool `json:"truncated"`
	// FreshnessSeconds is the lease window, published so a connector does not have
	// to guess how stale an omitted peer had to be.
	FreshnessSeconds int `json:"freshnessSeconds"`
}

// AuthorizationCandidateList and CapabilityEvidenceSelfReported are the exact
// strings the API contract specifies.
const (
	AuthorizationCandidateList         = "candidate-list-not-authorization"
	CapabilityEvidenceSelfReported     = "self-reported-not-attested"
	IdentityEvidenceEnrollmentBound    = "enrollment-bound-not-hardware-attested"
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
	if out.CapabilityEvidence != CapabilityEvidenceSelfReported ||
		out.IdentityEvidence != IdentityEvidenceEnrollmentBound ||
		len(out.Peers) > maxDirectoryPeers {
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
