# OS logos for instance icons

Put the official logo for each system here as a square PNG (128×128 or larger, transparent
background), named exactly:

| File | Shown for |
|---|---|
| `os-ubuntu.png` | Ubuntu VMs and dev containers |
| `os-debian.png` | Debian |
| `os-home-assistant.png` | Home Assistant |
| `os-jellyfin.png` | Jellyfin |
| `os-fedora.png`, `os-alpine.png`, `os-rocky.png`, `os-arch.png` | those systems, if offered |
| `os-devcontainer.png` | generic dev containers |

Get them from each project's official brand/press page and follow its trademark guidelines.
`macos/scripts/update-helper.sh` copies this folder into the app's Resources; any file that is
missing falls back to a generic symbol.
