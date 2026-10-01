# neXal@home for Mac 0.4.0

## New
- **Throwaway hosts (preview).** This Mac can lend a disposable Linux machine to you and your team. Create one from the iPhone app or the dashboard, connect over SSH, Files or Screen Sharing, and delete it when you are done. Persistent machines keep their disk; temporary ones expire after up to one week.
- **Live build progress.** While a throwaway host is being set up, the app shows each step (download, disk, first boot, joining your network) with a progress bar.
- **Choose which Macs lend capacity.** Resource sharing is on by default for your Macs and can be turned off per Mac.
- **Rename a throwaway host** and change when it expires, once it is running.
- **Self-updating image catalog.** New Debian and Ubuntu releases appear automatically; checksums are verified before use.

## Changed
- The VM helper (`nexal-vmhost`) now ships inside the app, so no separate install is needed.

## Fixed
- Failed throwaway hosts can now be deleted.
- Qcow2 cloud images are converted in the app; no extra tools are required.

## Upgrade notes
- Update the neXal server (including database migrations 0074-0078) **before** installing this version; older servers do not know the new throwaway-host fields.
- Dev containers additionally need a container runtime on the Mac and are not part of this preview.
- Throwaway hosts have not been verified on every Mac model yet; treat this as a preview.
