// Package pool provides local private-pool building blocks, not a distributed
// filesystem or execution engine. All network transports and runtime launchers
// are deliberately outside this package.
//
// A Store owns an explicitly selected dedicated directory. Objects are immutable
// SHA-256 blobs; "protected" is a storage class, not a durability assertion. Only
// a manifest backed by fresh receipts from two distinct, explicitly enrolled
// device identities may be described as protected. Signed receipts establish
// which trusted device made a statement, not remote disk/hardware attestation.
//
// Admission is the single local accounting authority for private/public compute
// and persistent storage. It is not an OS resource limiter. Callers must stop
// expired/reclaimed processes and fence stale attempts before reusing capacity.
//
// Requires Go 1.25+ for descriptor-rooted filesystem operations. Linux and macOS
// are supported. Optional peer transport uses TLS 1.3 with enrolled Ed25519 key
// pins and explicit interfaces/peer lists; no listener starts by default.
// No public participation, at-rest encryption, leader election or MLX execution
// is enabled by this package.
package pool
