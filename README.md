# Nexal Connector

Private Mac connector and experimental LAN CPU pager. **Engineering preview:
not added macOS RAM/VRAM, a production marketplace, or verified PQ dispatch.**

## M4 donor

Fresh checkout; requires GitHub SSH access. On the M4:

```bash
git clone git@github.com:keithknott26/nexal-connector.git "$HOME/Downloads/nexal-connector" &&
S="$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" &&
bash "$S" --enroll-platform --profile private-lan --name "M4 mini" &&
bash "$S" --donor
```

Enrollment uses a hidden invitation prompt. Donor mode shows numbered IP,
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
Add `--prepare-only` to stop before
invitation entry. Use `--network-info` instead of enrollment/donor flags to list
numbered connections without starting a listener.

**Shared pilot ready for owner acceptance (2026-09-19):**
[Open the dashboard](https://nexal-coordinator-dev.nexal.systems/) and use its
Nexal owner credential, not a Cloudflare API token, to authorize the tab.
Dashboard assets, relay and migration 0006 are deployed. The owner reports
successful login and an M4 entry; fresh two-Mac enrollment and LAN transfer
still need testing. See [troubleshooting](SETUP-TROUBLESHOOTING.md).

## iPhone pairing app

[nexal@home source](https://github.com/keithknott26/nexal-ios) is being developed
in a separate private repository. SMS login, donor/receiver QR pairing and
App Store distribution are **not live**. An App Store QR will be added only
after the actual listing exists; no placeholder store link is presented as real.

## More

- [Mac app and developer setup](DEVELOPER-GUIDE.md)
- [LAN test runbook](experiments/tcp-pager/MAC-TEST-RUNBOOK.md)
- [CLI contract](connector/CLI-CONTRACT.md) · [Security](SECURITY.md)
- [Engineering handoff and release limits](CURRENT-HANDOFF.md)
