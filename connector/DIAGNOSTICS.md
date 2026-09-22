# neXal Connector setup diagnostics

`nexal doctor` generates a read-only JSON report that is useful before starting
the agent. It does not require an enrollment credential or a running coordinator,
and it does not automatically repair or upgrade the machine.

## Run on your Mac

From the connector repository, after building the reviewed source:

```sh
cd connector
go build -trimpath -o build/nexal ./cmd/nexal
./build/nexal doctor
```

Use `--config /absolute/path/config.json` to inspect a non-default configuration.
For explicit, local hardware diagnostics:

```sh
./build/nexal doctor --probe
```

Default mode reads only the nonsecret config file. Probe mode additionally uses
the same bounded `ioreg`, `vm_stat` and `sysctl` observations as the agent, with
per-command deadlines and an overall seven-second context. These commands run
only with a valid configuration on Apple Silicon macOS. No model or workload is
loaded, no cloudflared process is run, and no local API or network is contacted.

## Interpret the report

- **pass:** The particular local check succeeded. It is not global readiness.
- **warning:** The observation deserves attention, such as missing enrollment,
  resumed saved policy, unavailable telemetry, owner activity or low free memory.
- **not_checked:** Deliberately untested, including credentials, connectivity and
  live tunnel negotiation. `--probe` is required for telemetry.
- **blocked:** An invalid/unavailable configuration, an impossible RAM policy,
  or a release gate. Production dispatch remains blocked in this release.

`productionReady`, `credentialsRead`, `networkContacted` and
`configurationModified` are false. Exit 0 means the JSON report was produced,
not that every check passed. Invalid flags or failure to write output return
nonzero. Missing/invalid config is reported as a blocked check without disclosing
the raw filesystem error. Schema version is 1; check IDs are stable identifiers.

An enrollment marker is not proof a host token still works. Saved pause policy
is not live agent status. Point-in-time headroom is not permission to run a job;
the scheduler must gather fresh telemetry, check owner consent and confirm leases.
Run `nexal status` separately when you intentionally want an authenticated local
API check; that separate command reads the local admin credential.

## Privacy and limitations

Reports exclude host names/IDs, endpoints, filesystem paths, raw diagnostic output,
keys, tokens and credential-store contents. Numeric resource policy is included
when configuration is valid; probe mode summarizes available-memory/idle checks
without including exact observed idle duration. Review reports before sharing.
No report file is created automatically.

This diagnostic is not a malware scanner, full software inventory, installer,
Keychain test, native SwiftUI acceptance or proof of post-quantum security.
The source has unit/race tests and a Darwin ARM64 cross-build in Linux; native
telemetry and recovery on the actual M4 and M2 must still be tested.
