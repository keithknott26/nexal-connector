# Private service discovery

neXal does not route multicast DNS (`224.0.0.251`) between networks and does
not describe remote sites as one Ethernet segment.

The provider may expose SMB and Screen Sharing through three explicit seams:

1. A tenant-private, split-horizon Wide-Area Bonjour browse domain publishes
   sanitized `_smb._tcp` and `_rfb._tcp` records.
2. An optional per-site gateway browses local advertisements and republishes
   short-lived sanitized records; it does not proxy traffic or change macOS
   Sharing settings.
3. An authenticated multicast-over-unicast Discovery Bridge carries only the
   allowlisted service envelopes. Every envelope binds origin, site and network
   IDs and has a monotonic sequence, hop limit, TTL, and deterministic dedup key.

The stable customer address is always
`<short-id>.mesh.nexal.systems` (for example, `xycs14.mesh.nexal.systems`). It is supplied by the platform/mesh
adapter, resolved by tenant-private DNS, and reported as `provisioned`,
`resolving`, `ready`, `stale`, or `conflict`. Customer surfaces reject upstream
provider hostnames.

TCP 445 and 5900 authorization remains server-side and denied by default. The
connector only shows an open/copy action when policy is authorized, macOS
reports the corresponding service enabled, and the private neXal hostname is
ready. It never enables File Sharing, Screen Sharing, Remote Management, or
firewall rules.

For a strict network the privileged adapter persists its startup plan and
reapplies `up --enable-rosenpass` with the scoped setup and management values
after every daemon restart. Permissive Rosenpass mode is forbidden. Command
success and saved configuration are not protection evidence: every connected
peer must report live quantum-resistance status. Missing or stale evidence
degrades the peer and blocks compute, SMB, and Screen Sharing.
