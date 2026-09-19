# Nexal Connector migration

This is a product rename, not a legal entity rename. KWK, LLC remains the stated
business entity; Nexal Connector is the app for the intended nexal.systems service.

The new CLI is `nexal`, native app is `Nexal Connector.app`, and bundle identifier
is `systems.nexal.connector`. Production state uses
`~/Library/Application Support/Nexal`; explicit local development uses
`~/Library/Application Support/Nexal-Local-Preview`.

Existing KWK configuration, credential files and Keychain entries are not copied
or deleted. Re-enrollment is required for new state. Keep old state intact until
a reviewed migration/export mechanism is implemented. The local port is still
8788, so stop the old connector before starting the new one.

Clone nexal-platform and nexal-connector as siblings. Their COMPATIBILITY markers
must match. A marker is not a signature, security audit or production approval.
The shared CONTRACT.md is an API snapshot; coordinate breaking changes across
both repositories.
