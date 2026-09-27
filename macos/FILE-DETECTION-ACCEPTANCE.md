# File-detection development artifact acceptance

Verified 2026-09-26.

**Artifact:** `/Users/kknott/Developer/nexal-connector/macos/build/neXal-Connector.app`

The standard `macos/scripts/package-app.sh` produced a universal development app. Its native UI, Go connector helper, embedded network runtime, and YARA-X helper each contain **arm64 and x86_64** slices. Swift executable minimum OS was checked as macOS 14. The final Go helper was rebuilt for both architectures after the scan-root replacement fix, then the helper and bundle were signed again.

The bundle is **ad-hoc signed**, and `codesign --verify --deep --strict --verbose=2` passes. It is **not Developer ID signed or notarized** and is not a public distribution release. YARA-X's required JIT/executable-memory entitlements are scoped to its helper. YARA-X and bundled-rule license notices are included.

## Checks performed

- Full native packaging suite: **176 tests passed**. Scanner settings had **7 focused tests passed** before packaging, covering argument separation, pause preservation, explicit baseline approval, partial/expired status, and local findings decoding.
- Real smoke test invoked the **signed bundled connector CLI**, which selected its sibling bundled YARA-X **1.20.0** automatically. No custom engine path was needed.
- The private fixture contained only the harmless **EICAR scanner test string** and benign plain text. Two files were scanned; exactly one `nexal_eicar_test` finding was produced with `testOnly: true`. The benign file produced no finding. Rules version was `nexal_rules_v1`.
- `security findings` returned the EICAR file's local path. `security status` omitted the local findings ledger. This checks the local CLI separation; it is not an independent network packet capture.
- Pausing returned `enabled: false` and preserved the configured scan folder.
- Final reviewed `scanner.go` SHA-256: `78ef49058f33eb207b88348d5f5eeb4b534b6c537d6646fadfc27004fcecbe4f`.
- Final fixture directory: `/private/tmp/nexal-scanner-smoke-jn4kh1hh`. It contains harmless test inputs and private local scanner state, not real malware.

No installed application was replaced. The app UI was not launched, and no existing user configuration was activated or changed. Physical UI interaction, Intel execution, and notarized distribution remain separate acceptance checks.

## Activation in the development app

Open the development artifact when ready, then expand **File scanning & code-style review** in the Network panel. Choose specific folders and select **Enable scanning**. Use **Scan now** for a bounded manual pass; the UI allows two minutes for scanning and reports limited coverage or unavailable engines honestly. **Recent local findings** identifies flagged files without opening or executing them. Paths stay in the local ledger; the UI caps the list at 100 and explains response truncation.

Approve a code-style baseline only after reviewing and trusting the scripts in the saved folders. The explicit confirmation records style measurements; it does not certify malware-free files or establish authorship. **Pause scanning** preserves saved folders. No whole-device coverage, automatic mitigation, or continuous runtime monitoring is implied.
