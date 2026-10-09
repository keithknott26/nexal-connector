// Package inference is the PREFLIGHT half of the "Add AI Inference Model"
// feature. It answers one question honestly before anything is installed: given
// this Mac and the Macs on the owner's neXal mesh, which catalog models could run,
// where, and what is stopping them.
//
// What it is built from (the preflight half invents no data and opens no network socket):
//
//   - host facts and link facts are read through the HostSource and LinkSource
//     interfaces. The live implementation (live.go) decodes the connector's own
//     local /v1/status document, which already carries the policy (owner memory
//     cap and reserve), telemetry, the mesh peers (path, latency, PQ state,
//     PathFlapsLastHour, measured BandwidthMbps) and the coordinator-reported
//     details of other hosts (chip, OS, memory, disk).
//   - the MLX runtime's own `probe` path is run through RuntimeProber, after the
//     owner policy's SHA-256 pins for the interpreter and the runtime tree match.
//   - memory fit is decided by pool.PlanMLX, never re-derived here. This package
//     only builds the planner's input (pool.RankMemory per rank, pool.MLXPeer per
//     host) from catalog estimates and host facts.
//
// The INSTALL half is separate code with a different network posture:
//
//   - hf.go is the only file that opens sockets, used only by `pin` and
//     `install`: https to huggingface.co / *.hf.co, no credentials, redirect
//     allowlist, hard size caps, resumable .part files, SHA-256 before rename.
//   - pins.go merges <config dir>/inference/pins.json (facts read from one
//     immutable commit) over the embedded, unpinned catalog; install.go refuses
//     anything not fully pinned and ends at state installed_unmeasured, because
//     the manifest's memory terms are catalog estimates and the runtime requires
//     measured ones. manage.go is status, verify and remove.
//
// What it deliberately does NOT claim: that model weights are installed, that
// multi-host execution exists (it does not; see sharing.go), or that a number
// marked "estimate" is a measurement.
package inference
