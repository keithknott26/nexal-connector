# neXal@home for Mac 0.3.0

## New
- **Honeypot.** Optional decoy services (SSH, Telnet, RDP, SMB, VNC and web) that nothing legitimate should use. Any connection from another computer is reported as a security alert. Listens only on this Mac's neXal network and private local-network addresses, never a public address, and never accepts logins or runs commands. Turn it on in Settings › Security. Source addresses stay on the Mac.
- **Honeypot status** is now shown for each Mac in neXal@home on iPhone and in the dashboard.

## Changed
- The app is now called **neXal@home** on the Mac, matching the iPhone app. The installed file is still named neXal-Connector.app, so existing installs update in place.
- Clearer, plain-language wording throughout the app.
- The pairing screen notes that paired Macs can process your de-identified work if available.

## Removed
- Folder file scanning (YARA-X) and process inspection. neXal's security features are Host watermarks and the honeypot; neXal does not scan files for malware. The bundled YARA-X helper is no longer included.
- The background security-learning client.

## Upgrade notes
- Update the neXal server (including database migrations 0065–0069) **before** installing this version on your Macs; older servers reject the new honeypot status fields.
- Connector version is now reported as 0.3.0 (previously 0.1.0).
