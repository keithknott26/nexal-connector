# neXal@home connector

The connector is the trusted Go host component for neXal@home. It joins a Mac or
Linux computer to the customer's private network, reports verified tunnel and
route state, enforces host policy, presents approved local services, and provides
the execution boundary for MCP tools, tripwire and honeypot alerts, and the
owner's own distributed work.

Implemented foundations include secure QR/manual pairing, strict Rosenpass mesh
startup, tunnel evidence, coordinator-driven access rules, SMB and screen-sharing
presentation, wide-area discovery contracts, privacy-preserving security baseline
states, and signed encrypted model/rule bundle verification with anti-rollback.

Direct private-mesh MCP, live multi-site route evidence, distributed compute
and CPU paging still require integration or physical-host acceptance. CPU paging remains research; it is not
additional macOS RAM or VRAM.

Security scope is limited to tripwires (Host watermarks: decoy files), an opt-in
decoy honeypot, and alerts. The connector does not provide antivirus, malware or
file scanning, process inspection, or network traffic inspection. See
[FEATURES.md](FEATURES.md#security-contract).

Go is the primary host language. Swift is limited to the native macOS interface.
The Python MLX adapter under `runtimes/` is an isolated experimental compatibility
layer for Apple's Python-facing MLX tooling; it is not the connector control plane
and should be replaced if a reviewed native interface reaches feature parity.

## Production network boundary

The production peer network is the managed neXal mesh over WireGuard. Connector
startup and reconnect always require Rosenpass;
saved configuration or command success alone is not proof of protection. Peer
traffic, including MCP requests and results, goes directly to the destination's
private mesh address. Cloudflare coordinates enrollment, private address
metadata, tool/schema pins, signed short-lived grants, policy, revocation and
audit, but is not in the peer payload path. The connector reports runtime path
evidence as `direct_mesh` or `relay_mesh` and exposes relays with neXal product
terminology. No second peer overlay is part of the production design.

## M4 donor

Fresh checkout; requires GitHub SSH access. On the M4:

```bash
git clone git@github.com:keithknott26/nexal-connector.git "$HOME/Downloads/nexal-connector" &&
S="$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" &&
bash "$S" --enroll-platform --profile private-lan --name "M4 mini" &&
bash "$S" --donor
```

Enrollment opens the Hosts page in your default browser. Enter the neXal owner
credential there, generate a one-use invitation, then paste the invitation into
Terminal's hidden prompt. The invitation starts with `enr_`; **do not paste
the owner token into Terminal**. No credential is put in the browser URL.
Before requesting an invitation, setup checks connectivity without credentials;
if it fails, resolve the reported connection issue rather than generating codes.
Donor mode shows numbered IP,
Ethernet/Wi-Fi and interface labels; choose a number and leave its terminal open.
Stop an existing donor before starting another. Restarting produces a new bundle.

## M2 receiver

Fresh checkout; on the M2:

```bash
git clone git@github.com:keithknott26/nexal-connector.git "$HOME/Downloads/nexal-connector" &&
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" --enroll-platform --profile private-lan --name "M2 mini"
```

This enrolls/prepares the M2; it does not start receiving pages. Follow
[bundle delivery and receiver startup](experiments/tcp-pager/PLATFORM-DELIVERY.md)
after both hosts and the shared coordinator relay are ready. Do not run donor
mode on the M2.

The current relay requires both Macs enrolled: the M2's own platform identity
authorizes delivery, while the M4's client bundle authorizes private pager access.
Downloading the bundle is not platform enrollment. Phone/QR automation of these
steps is not yet connected end to end.

## Existing checkout and options

For an existing checkout, update and enroll without cloning again:

```bash
git -C "$HOME/Downloads/nexal-connector" pull --ff-only &&
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" --enroll-platform --profile private-lan --name "M4 mini"
```

Use `"M2 mini"` on the M2; on the M4, start `--donor` afterward as above.
Enroll the M4 first, refresh the dashboard and match its printed host ID, then
enroll the M2 with a **new invitation**. `--profile private-lan` uses a separate
configuration on each Mac at
`~/Library/Application Support/Nexal-Profiles/private-lan/config.json`.
Original profiles and Keychain credentials remain untouched; old dashboard
entries are not automatically revoked. Rerunning the same profile does not
create another host. Use this exact config path for bundle transfer.
Add `--prepare-only` to stop before browser opening and invitation entry, or
`--no-browser` to open the printed dashboard address yourself. Browser-opening
failure is nonfatal. An interactive rerun of an enrolled profile opens Hosts
for inspection without requesting another invitation.
Use `--network-info` instead of enrollment/donor flags to list
numbered connections without starting a listener.

**Shared pilot ready for owner acceptance (2026-09-19):**
[Open the dashboard](https://dashboard-dev.nexal.systems/) and use its
neXal owner credential, not a Cloudflare API token, to authorize the tab.
Dashboard assets, relay and confirmed host deletion are deployed. Both Macs
have saved identities, currently both labeled `M4 mini`; compare IDs and CPU/RAM
before enrolling again or deleting. Native LAN transfer still needs testing.
See [troubleshooting](SETUP-TROUBLESHOOTING.md).

## iPhone pairing app

[neXal@home source](https://github.com/keithknott26/nexal-ios) is being developed
in a separate private repository. Email-code login (Postmark), donor/receiver QR pairing and
App Store distribution are **not live**. An App Store QR will be added only
after the actual listing exists; no placeholder store link is presented as real.

## More

- [Mac app and developer setup](DEVELOPER-GUIDE.md)
- [LAN test runbook](experiments/tcp-pager/MAC-TEST-RUNBOOK.md)
- [Pager connection troubleshooting](experiments/tcp-pager/CONNECTION-TROUBLESHOOTING.md)
- [CLI contract](connector/CLI-CONTRACT.md) · [Security](SECURITY.md)
- [Engineering handoff and release limits](CURRENT-HANDOFF.md)
