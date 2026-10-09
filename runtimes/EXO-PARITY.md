# exo parity: what is verified, what is possible, what is not built

Source reviewed: exo-explore/exo @ 21a54c5e (Apache-2.0), 2026-10-09. Nothing here
is built yet; this is the verified basis for building it.

## What exo provides (source refs)

| Feature | Where |
|---|---|
| Auto-discovery of Macs on one network | rust/networking/src/discovery.rs |
| Topology-aware pipeline + tensor sharding (auto placement) | src/exo/master, POST /place_instance |
| RDMA over Thunderbolt 5 (macOS 26.2+, full TB5 mesh, identical OS builds) | README, tmp/set_rdma_network_config.sh |
| OpenAI Chat/Responses, Claude Messages, Ollama APIs | src/exo/api/main.py routes ~L344-409 |
| Web dashboard + macOS app | dashboard/, app/EXO |
| HF model download, custom models (POST /models/add), image models | src/exo/download, /models/* |
| Continuous batching, traces, topology/state view (GET /state) | src/exo/worker, /v1/traces |

## Ports and flags (verified)

API + dashboard TCP 52415 (`--api-port`), zenoh TCP 52414 (`--zenoh-port`),
discovery UDP 52413 (`--discovery-port`), `--namespace` / `EXO_ZENOH_NAMESPACE`
(`EXO_LIBP2P_NAMESPACE` now raises), `--no-worker`, `--offline`, `--no-downloads`.

## Blocker 1: exo cannot cluster across the nexal mesh today

- Discovery is IPv6 link-local multicast (ff12::e0a1:de89, UDP 52413), joined only on
  interfaces that have a non-loopback IPv6 address. Peers then connect over zenoh
  TCP to the sender's IPv6 address.
- The nexal mesh is an IPv4 100.x WireGuard overlay: no IPv6, no multicast.
- exo's `--bootstrap-peers` / `EXO_BOOTSTRAP_PEERS` raises "temporarily removed"
  (src/exo/main.py) and TODO.md lists it as broken.
- So exo works for Macs on one LAN or joined by Thunderbolt/Ethernet. Macs that are
  only reachable through the mesh will not discover each other.

## Blocker 2: exo's API is unauthenticated and bound to 0.0.0.0

`run_api` binds `0.0.0.0:52415`, there is no auth, and CORS allows `*` with
credentials. Anyone on the LAN, or any web page open in a browser on that Mac, can run
inference, place or delete instances, add models and start downloads. A nexal-managed
exo must restrict 52415 to loopback or owner-approved peers (pf rule, or launch exo
behind a loopback-only proxy). Not implemented; do not describe it as safe until it is.

## Parity matrix

| Capability | Status |
|---|---|
| Single-LAN clustering, sharding, APIs, dashboard | exo provides; nexal would install, launch, firewall and monitor it |
| Hash-verified install of exo | nexal can build (maintainer must pin a release on a networked machine) |
| Cross-mesh clustering | NOT possible until the mesh carries IPv6 and a discovery bridge relays Hello/WhatsUp datagrams, or exo fixes bootstrap peers |
| RDMA | owner-side setup (Recovery `rdma_ctl enable`, TB5 cabling); nexal can only detect and advise |
| nexal's own MLX runtime (`runtimes/nexal_mlx`) | batch CLI, single host, 4096/512 token caps; no sharding, no server, no UI. Not a parity path |

## Phases

1. Read-only detect / status / preflight: installed? running? cluster state from
   loopback `GET /state`, `/v1/models`; per-peer blockers (macOS >= 26.2, identical OS
   builds, LAN-direct vs mesh-only, API exposure). No installs or launches.
2. Managed exo: pinned install, launchd start/stop, firewall for 52413-52415,
   namespace per owner, Swift UI with dashboard link and API snippets.
3. Cross-mesh clustering: IPv6 overlay in the mesh engine plus discovery bridge.
   Large; needs engine work.

## Unknowns

State JSON field shapes beyond state.py, exo release cadence and a stable pinned
artifact, behaviour of the macOS app's network LaunchDaemon next to the nexal mesh
interface, and real-world throughput on 1 GbE (exo publishes only Thunderbolt numbers).
