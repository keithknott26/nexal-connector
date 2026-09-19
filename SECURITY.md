# Nexal Connector security

Keep this repository private. Never commit real tokens, Keychain exports,
private keys, feed credentials, model customer data or device configuration.
The `.gitignore` file and secret checks are defense in depth, not a guarantee.
Report issues privately to the owner without attaching live credentials.

The connector is an engineering preview with production execution disabled.
Go admission checks and an unsigned Swift app are not hostile-code isolation.
Do not disable Gatekeeper, SIP, transport verification or owner reclaim to make
a test pass. See `connector/README.md` and `runtimes/RELEASE-GATES.md`.

GitHub Actions are initially disabled. The manual macOS job requires owner review
of CI spending before use. CODEOWNERS does not itself enforce branch protection.
