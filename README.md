# Nexal Connector

Private Mac connector and experimental LAN CPU pager. **Engineering preview:
not added macOS RAM/VRAM, a production marketplace, or verified PQ dispatch.**

## M4 donor

Fresh checkout; requires GitHub SSH access. On the M4:

```bash
git clone git@github.com:keithknott26/nexal-connector.git "$HOME/Downloads/nexal-connector" &&
S="$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" &&
bash "$S" --enroll-platform --name "M4 mini" &&
bash "$S" --donor
```

Enrollment uses a hidden invitation prompt. Donor mode shows numbered IP,
Ethernet/Wi-Fi and interface labels; choose a number and leave its terminal open.
Stop an existing donor before starting another. Restarting produces a new bundle.

## M2 receiver

Fresh checkout; on the M2:

```bash
git clone git@github.com:keithknott26/nexal-connector.git "$HOME/Downloads/nexal-connector" &&
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" --enroll-platform --name "M2 mini"
```

This enrolls/prepares the M2; it does not start receiving pages. Follow
[bundle delivery and receiver startup](experiments/tcp-pager/PLATFORM-DELIVERY.md)
after both hosts and the shared coordinator relay are ready. Do not run donor
mode on the M2.

## Existing checkout and options

For an existing checkout, update and enroll without cloning again:

```bash
git -C "$HOME/Downloads/nexal-connector" pull --ff-only &&
bash "$HOME/Downloads/nexal-connector/experiments/tcp-pager/scripts/setup-lan-macos.sh" --enroll-platform --name "M4 mini"
```

Use `"M2 mini"` on the M2; on the M4, start `--donor` afterward as above.
Enrollment preserves existing profiles. Add `--prepare-only` to stop before
invitation entry. Use `--network-info` instead of enrollment/donor flags to list
numbered connections without starting a listener.

**Shared pilot ready for owner acceptance (2026-09-19):**
[Open the dashboard](https://nexal-coordinator-dev.nexal.systems/) and use its
Nexal owner credential, not a Cloudflare API token, to authorize the tab.
Dashboard assets, relay and migration 0006 are deployed. Owner login and the
two-Mac transfer still need testing; see [troubleshooting](SETUP-TROUBLESHOOTING.md).

## More

- [Mac app and developer setup](DEVELOPER-GUIDE.md)
- [LAN test runbook](experiments/tcp-pager/MAC-TEST-RUNBOOK.md)
- [CLI contract](connector/CLI-CONTRACT.md) · [Security](SECURITY.md)
- [Engineering handoff and release limits](CURRENT-HANDOFF.md)
