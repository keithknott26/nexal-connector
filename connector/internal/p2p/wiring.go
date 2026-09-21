package p2p

import (
	"nexal/connector/internal/config"
	"nexal/connector/internal/pool"
	"nexal/connector/internal/stun"
)

// The config→Options bridge lives HERE, not in internal/config, because
// internal/config must stay dependency-free: it is imported by internal/client
// and therefore (via internal/discovery) by internal/pool's test binary, and
// pulling libp2p into that chain would put the dependency everywhere instead of
// in one package. p2p importing config is safe in the other direction — config
// imports nothing internal.
//
// The ONE source of truth for the ceiling defaults and bounds stays
// Limits.Normalize. config.P2P carries the owner's raw numbers and nothing else,
// which is why zero passes straight through here: Normalize decides what zero
// means, in one place, tested in one place.

// OptionsFrom builds Options from the owner's config plus the caller-supplied
// identity, allowlist and optional STUN observation.
//
// It cannot turn the feature on by itself: Enabled comes from the config block
// alone, and a nil block means off. It also cannot turn authorization on — the
// Authorizer is passed in by the agent from the coordinator's directory, because
// a constructor that could build its own allowlist is a constructor that could
// widen one (HARDENING-PLAN §30.2).
func OptionsFrom(c *config.P2P, id pool.Identity, authz Authorizer, observation *stun.Result) Options {
	o := Options{Identity: id, Authorizer: authz, STUN: observation}
	if c == nil || !c.Enabled {
		return o
	}
	o.Enabled = true
	o.Listen = append([]string(nil), c.Listen...)
	o.Limits = Limits{
		MaxRelayedConns:        c.MaxRelayedConns,
		MaxCircuitSeconds:      c.MaxCircuitSeconds,
		MaxCircuitBytes:        c.MaxCircuitBytes,
		MaxTotalBytes:          c.MaxTotalBytes,
		OfferRelayService:      c.OfferRelayService,
		MaxServiceReservations: c.MaxServiceReservations,
	}
	return o
}
