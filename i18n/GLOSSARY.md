# Glossary and style

## Never translate (keep exactly as written)

neXal, neXal@home, neXal Home, neXal Storage, neXal Relay, ML-KEM-1024, Level 5, NIST,
NetBird, Rosenpass, SMB, VNC, SSH, DMG, SHA-256, WoL, IP, LAN, DNS, VM, CPU, RAM, GitHub.
Product and company names, command lines, file paths, URLs and anything in `code font`.

## Use Apple's own wording for Apple features

Match the exact labels macOS/iOS show in the target language: Time Machine, Finder, System
Settings, Full Disk Access, Login Items, Screen Sharing, File Sharing, Keychain, Bonjour,
Wake on LAN. Look them up in the target-language macOS UI, do not invent translations.

## Terms to translate consistently (pick one rendering per language, then reuse it)

| English | Meaning |
|---|---|
| peer | another device on the user's private network |
| mesh / mesh network | the private encrypted network between the user's devices |
| tunnel | the encrypted link between two devices |
| host (throwaway host) | a short-lived virtual machine or dev container |
| gateway | the always-on neXal server that carries storage and exit traffic |
| quantum-protected / post-quantum | protected against future quantum computers |
| relay | a server that forwards traffic when no direct path exists |
| pairing / enrollment | adding a device to the user's network |

## Style

* Friendly, direct, calm. Address the user with the polite singular form where the language has one
  (Sie, vous, usted, Вы, ...), unless an informal register is clearly standard for apps in that language.
* Short, sentence-case UI text. Buttons are verbs. Do not add words that are not in the English.
* Keep length close to the English; labels sit in narrow rows. Prefer the shorter natural option.
* Keep `…` (single ellipsis character) where English uses it.
