# Private pager connection troubleshooting

Enrollment, platform bundle delivery, TCP reachability and authenticated paging
are separate checks. A successful `nc` probe is not a TLS or paging success.

## Current owner result

September 19, 2026: M2 receiver (8 GiB) received and imported the M4 Pro donor
bundle. First paging connection timed out; a later attempt was reset during TLS.
Both raw TCP probes were reported successful. No pages were transferred, no
native cases executed, and no OS/GPU memory expansion occurred.
The reset's underlying cause is not established. Do not call this a proven
certificate, firewall, hybrid-key-exchange or macOS failure.

## Updated diagnostics

Receiver errors now distinguish `TCP connect`, `TLS handshake` and
`authenticated donor greeting`. The original combined three-second TCP/TLS
deadline remains unchanged.

The lab donor prints fixed-label JSON events for TCP acceptance and TLS result.
At most 32 events and one suppression notice are printed per run. No raw TLS
errors, certificate contents, peer addresses or keys are logged.

- `tcp_accept / accepted`: this donor accepted a socket, not authenticated yet.
- `tls_handshake / authenticated`: mutual TLS completed, not a paging result.
- `timeout`, `connection_reset`, `connection_closed`: the indicated transport
  symptom; compare both endpoints and their clocks. These do not identify the cause.
- `certificate_time_invalid`: check both Mac clocks and certificate lifetime.
- `certificate_untrusted` or `certificate_name_invalid`: check that the receiver
  uses the current donor's bundle. Never bypass certificate validation.
- `connection_rejected`: other error; the fixed label deliberately does not
  expose peer-supplied TLS text or claim a more specific cause.

If M2 fails while this donor prints no acceptance event, investigate endpoint
selection, listener ownership or the network path before changing certificates.
An intermediary can accept/reset traffic, so raw TCP success alone is insufficient.

## Restart the donor without generating new certificates

Stop only the previous pager donor with Ctrl+C, not the normal connector.
After updating the repository, run on the M4:

```sh
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/resume-donor-macos.sh" \
  "/ABSOLUTE/EXISTING/DONOR/STATE/run"
```

Use the state directory one level above its `client` folder. This builds the
diagnostic version and explicitly serves existing state for at most 30 minutes
and 16 authenticated sessions. It never kills processes, rewrites keys, changes
firewall settings, re-enrolls, or starts on login. Bind conflicts fail safely.
Existing credentials must remain valid and the endpoint must still belong to M4.
The store is fresh per session; this does not recover old remote memory.

On M2, update the repository and rerun against the previously imported credentials:

```sh
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/lan-receiver-macos.sh" \
  "/ABSOLUTE/RECEIVER/EVIDENCE/run/credentials"
```

This rebuilds the receiver, creates a fresh evidence directory and asks for the
public CA fingerprint from the donor terminal. No transfer is needed when the
same valid donor state is resumed. A newly generated donor state needs its own
new bundle. Keep private keys and certificate contents out of chat and GitHub.

## Validation

Linux Go race tests, vet, eight actual CLI/transport tests, shell syntax and Darwin
arm64 cross-compilation passed. Existing missing-client-certificate, wrong-CA and
classical-only rejection tests still pass; no TLS downgrade or timeout increase.
The resume test verifies unchanged key/manifest files and successful authenticated
portable paging after restarting with existing donor state.
Native Mac diagnostic output and a successful two-Mac paging run remain unverified.
