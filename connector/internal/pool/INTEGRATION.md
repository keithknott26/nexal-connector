# Private pool integration surface

Package: `nexal/connector/internal/pool`. Standard library only. Linux/macOS; Go 1.25+
(`os.Root.Link`), unified connector toolchain currently Go 1.26.

## APIs for connector/core

- `NewAdmission(AdmissionOptions)` creates one shared local capacity authority.
  `Reserve(owner, PrivateWork|PublicWork|MLXWork, Resources, ttl)`, `Release`,
  `Renew`, `Validate`, `Expire`, `Reclaim`, `Resume`, `SetCapacity`, `Snapshot`.
  Resources are CPU **millicores**, memory **bytes**, storage **bytes**.
  Public is disabled unless explicitly opted in. All stores should share this
  admission instance. Persistent disk charges survive store close and reclaim.
  Stop actual expired/reclaimed processes before reusing released capacity.
- `NewStore(StoreOptions{Directory, QuotaBytes, MinFreeBytes, MaxObjectBytes,
  Admission})`. Explicit dedicated 0700 directory; parent must exist; no symlink
  ancestors. Objects/files 0600. `Put`, `PutBytes`, `Read`, `ReadTo`, `Check`,
  `Receipt`, `EvictCache`, `Usage`, `Close`. SHA-256 lowercase hex, immutable.
  `Put(Protected, ...)` means durable-intent **local copy**, NOT two-copy safety.
- `NewIdentity`, `DeviceID`, `NewRegistry`, `Registry.Invite`, `Identity.Prove`,
  `Registry.Enroll`, `Member`, `Revoke`. Invite is owner-authorized, fingerprint
  pinned out of band, ten-minute maximum, one-use, signed challenge. LAN presence
  is not authentication. Registry is currently in memory; embedding agent must
  persist member/revocation configuration and keys under its recovery policy.
- `Publish(PublishRequest, registry, now, maxReceiptAge)` is manifest CAS.
  Protected publication needs signed verified receipts from **two distinct
  enrolled contributor IDs**. One receipt requires explicit `AllowSingleCopy`
  and is always labeled `single-copy`. `Latest`, `ManifestVersion`, `Tombstone`,
  `ProtectionStatus`, `PlanRepair`, `Backup`, `RestoreCatalog`.
  Always recompute current `ProtectionStatus`; `StateAtCommit` is history only.
  Receipts are trusted-host statements, not disk/hardware remote attestation.
  Enroll one reviewed identity per physical machine.
- `PlanMLX(PlacementRequest)` sums weights + KV + activations + temporary buffers
  + explicit safety per rank, subtracts existing reservations from owner-approved
  and measured memory budgets, checks pinned runtimes/backend, rejects base M4
  JACCL and missing TB5/macOS 26.2/RDMA/direct topology. No runtime is launched.

## Optional real private peer transport

`NewPeerServer(PeerOptions{Identity, Registry, AllowedPeers, Store})` returns a
dormant server. Caller explicitly binds a private unicast/loopback interface
using `net.Listen("tcp", "127.0.0.1:port")` or the selected private IP, then calls
`Serve(listener)`. Wildcard `0.0.0.0` / `::` listeners are rejected.
`Shutdown(ctx)` controls lifecycle. Nothing starts by importing/constructing it.

`NewPeerClient(PeerClientOptions{Identity, Registry, ExpectedDeviceID, Endpoint,
MaxObjectBytes})` accepts only HTTPS literal private/loopback IP + explicit port,
pins the enrolled Ed25519 identity, does not use environment proxies or redirects.
TLS 1.3 mTLS, short-lived self-signed identity certs, exact peer allowlists and
live per-request revocation checks. No claim of post-quantum peer transport.

Routes, authenticated and bounded:

- `PUT /v1/objects/{cache|protected}/{sha256}` with exact Content-Length; returns
  JSON `{blob, receipt}` only after full integrity verification and durable sync.
- `GET /v1/objects/{cache|protected}/{sha256}` streams integrity-verified bytes.

Use `PeerClient.Upload`, `Download`, or
`ReplicateProtected(ctx, sourceStore, localIdentity, registry, digest, clients,
now)`. Replication performs actual HTTPS upload and verifies the returned signed
receipt; it returns success only with local + remote distinct identities.
It does not auto-publish: feed receipts into `Publish`.

All explicitly allowed peers can read/write the selected store. Use a dedicated
store per mutually trusted project group. No project ACL middleware, anonymous
access, manifest endpoint, shell, job dispatch or listener discovery is included.
Network identity certificates expire after 24 hours: recreate peer objects to
rotate; identity keys remain stable. Requests have finite sizes/timeouts.

## Honest remaining boundaries

- Files are private by Unix mode, **not encrypted at rest**. `EncryptionKeyRef`
  is metadata only. Reviewed encryption/key lifecycle is still required.
- Backup is integrity-checked, version-chain-checked **catalog metadata** only.
  Independently retain object bytes, membership state and separately protected
  recovery keys. Checksums do not authenticate a hostile backup.
- Corruption/loss planning is implemented; repair must execute authorized copy,
  obtain a new receipt and append a new manifest. No fake repaired state.
- No OS hard limits, sandbox, process kill, distributed MLX execution, leader
  election, guaranteed physical-device uniqueness or production certification.
- Local recovery requires fencing the former controller; one exclusive file
  lock prevents simultaneous compliant writers to a store directory.

Source requirements read: repository `CONTRACT.md`, architecture private-pool
section and distributed MLX per-rank requirements. Package tests include real
loopback TLS transfers, fake acknowledgments, pin failures, revocation, quota/
headroom, corruption, replay, concurrency, traversal, symlink/hardlink protection,
catalog backup/restore and base-M4 rejection.
