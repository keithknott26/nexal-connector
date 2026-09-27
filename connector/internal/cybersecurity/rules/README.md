# Bundled local triage rules

These original MIT-licensed rules run on YARA-X 1.20.0. They are a small, reviewable starter set, not a comprehensive malware-family database. There are no downloaded third-party rules, automatic actions, source uploads, or runtime instrumentation.

| Rule | What to review | Limits |
| --- | --- | --- |
| `nexal_eicar_test` | Harmless EICAR engine-test pattern, always information/test-only | Not malware; test fixtures deliberately match |
| `nexal_script_download_execute` | A download command piped directly into a shell | Installers and documentation legitimately do this |
| `nexal_powershell_encoded_hidden` | PowerShell with encoded-command and hidden-window strings | Administrative tools may contain these strings |
| `nexal_python_reverse_shell` | Socket, descriptor redirection, shell and subprocess strings together | Security tools and examples may contain all four |
| `nexal_macho_persistence_credentials` | Mach-O magic plus LaunchAgents, RunAtLoad, Keychain access and shell strings together | Inert files and legitimate security tools may match; container magic is not proof of executable validity |

All non-test matches have **low severity** and require review. Matching bytes do not establish execution, intent, infection, or authorship. No rule identifies a specific malware family. macOS security controls remain necessary.

Style comparison is separately implemented in `style.go`; it compares six original formatting ratios against an explicitly approved average for the same script extension. At least three scripts with twenty nonblank lines each are required. Mean absolute normalized distance of 0.30 or greater yields an information-only `code_style_signal`; it never claims AI authorship. Features are tab indentation, indentation width, comment-line share, line length, underscored identifiers, and blank lines. These heuristics can change after legitimate formatting or refactoring.

Scans are opt-in for explicit directories, once per fifteen minutes or on demand. Each pass is bounded to two minutes, 2,000 regular files, 10,000 directory entries read in batches of 64, depth sixteen, four MiB per file and sixty-four MiB total. Symlinks, special files, oversized/unreadable files and the connector's state directory are excluded. Partial coverage is reported. Unsupported/binary/short files receive no style assessment but still receive byte scanning when within limits.

The cache uses content hashes, file extension, the engine binary hash, embedded rule bytes, style algorithm version and approved baseline ID. Merely touching or renaming an unchanged file does not rescan its bytes. Counts describe new engine inspections and new findings in the latest pass, not a lifetime threat total. A full outbox stops new inspections without claiming a clean pass.

Events contain rule/version identifiers, content SHA-256, and optional baseline ID/distance. They never contain file paths, source, or matched strings. Up to 100 events are retained durably before transmission. Retry uses the identical event. A later observation receives a fresh identifier. Events older than the API's seven-day acceptance window move into a local archive of the latest 100 entries; `expiredEvents` reports the cumulative count (bounded at one million). They are not silently discarded to unblock the queue. Disabling scanning retains state and evidence. Reconfiguring different roots invalidates the approved style baseline.

YARA-X's BSD-3-Clause license is included in the app as `YARA-X-LICENSE.txt`. The macOS bundle pins both official release archive SHA-256 values. The dedicated `yr` helper needs Wasmtime executable-memory entitlements for hardened-runtime scanning; those entitlements are not applied to the main app or connector helper. Distribution signing and notarization remain release checks.
