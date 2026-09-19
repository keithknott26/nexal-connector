# Cloudflare and cloudflared: provenance and current Nexal status

Checked September 19, 2026. The legitimate provider's name is **Cloudflare** and
its tunnel client is **cloudflared**; Cloudflare's own documentation explicitly
says that cloudflared is an open-source project maintained by Cloudflare
([official downloads](https://developers.cloudflare.com/tunnel/downloads/)).
The official client repository is
[`cloudflare/cloudflared`](https://github.com/cloudflare/cloudflared).

## Names are not interchangeable

- **Company and website:** Cloudflare, at
  [cloudflare.com](https://www.cloudflare.com/).
- **Tunnel software:** `cloudflared`, identified as the Cloudflare Tunnel daemon
  by the [official downloads documentation](https://developers.cloudflare.com/tunnel/downloads/).
- **Repository:** `github.com/cloudflare/cloudflared`, the
  [Cloudflare Tunnel client repository](https://github.com/cloudflare/cloudflared).
- **Different spellings:** Never treat `cloudfare`, `cloudfair`, or a similar
  spelling as an equivalent trusted publisher, domain or package. Similarity is
  not proof of common ownership. This rule is not an allegation about any
  particular different domain.

This first-party confirmation establishes the project's provenance. It does not
certify that any arbitrary file named `cloudflared` is authentic, vulnerability-free
or safe under an incorrect tunnel configuration.

## What is implemented in Nexal

The Go connector has an optional supervisor for an operator-provisioned binary.
It requires an absolute binary path, a pinned SHA-256, a matching architecture,
an exact release version and a source record under the exact GitHub owner/repo.
It checks private token-file permissions, uses a minimal environment, generates
restricted ingress configuration and launches with QUIC and `--post-quantum`.
No automatic download, trust establishment or tunnel provisioning is implemented.

Look-alike owner/repository names, deceptive hosts, URL user-info tricks,
plaintext sources, encoded path ambiguities, an unpinned latest-release URL and
query redirects are explicitly tested for rejection. These tests use harmless
local fixtures, not an official binary or a live Cloudflare connection.

Important limitation: the source URL, checksum and verification-method fields
are supplied by the operator. A matching checksum proves consistency with that
pin, NOT publisher identity by itself. Independent artifact provenance must be
established before trusting or executing a binary.

## Post-quantum policy

Cloudflare documents that QUIC tunnels use post-quantum cryptography by default
but may fall back; `--post-quantum` permits only PQ key agreements without
non-PQ fallback, and HTTP/2 does not support PQ key agreements
([official run parameters](https://developers.cloudflare.com/tunnel/reference/run-parameters/)).
Nexal configures strict QUIC/PQ and rejects observed downgrade diagnostics, but
this code policy has not been validated on the owner's network with a genuine
release. It is not proof of current negotiated cryptography.

The connector deliberately leaves independent verification and attestation
false. Outbound pilot job polling is a separate HTTPS path; the incoming tunnel
does not automatically protect it. No system-wide or all-hop PQ claim is made.

## Deployment state

- No live tunnel was established or verified for the owner's M4/M2 in this work.
- No `nexal.systems` DNS or hostname routing was changed.
- Cloudflare account inspection remains blocked by the previously recorded
  connector header-format error; see the platform domain checkpoint.
- Production marketplace dispatch remains disabled.
- The setup script does not install cloudflared, enroll a tunnel, or enable
  public exposure. `nexal doctor` does not execute cloudflared.

## Required before connecting the Macs

1. Resolve Cloudflare account access through the trusted connection flow; verify
   the intended account and domain. Never place credentials in a chat or repository.
2. Follow the official documentation to the official release, select an exact
   Apple-Silicon-compatible version, and establish artifact provenance. Verify
   available publisher signatures/attestations rather than assuming they exist.
3. Record both archive provenance/integrity and the extracted executable's digest.
   A tar archive checksum and the contained executable's checksum are different.
4. Inspect executable provenance before running `--version` or Nexal's
   `tunnel-check`, because even a version check executes the selected binary.
5. Provision least-privilege tunnel credentials and explicit ingress/Access
   controls with the owner's approval. Do not expose an administrative port as
   a substitute for implementing signed, authenticated job dispatch.
6. Verify live connectivity, downgrade refusal, token revocation, process restart
   and the actual crypto boundary on the M4/M2 network before enabling dispatch.

This checklist does not claim that these steps have already been performed.
