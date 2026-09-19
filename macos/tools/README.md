# Memory capability diagnostic

This is an experimental inspection tool, not an implemented memory-sharing
feature. Native Swift compilation and execution have not yet been verified for
this script; the development environment used for this addition is Linux.

## Run on the M4 first

From the root of this connector checkout:

```sh
xcrun swift macos/tools/probe-memory-capabilities.swift
```

No administrator access, recovery-mode setting, VM installation or 100 GB
allocation is requested. The script reads framework properties and writes JSON
to standard output; the Swift toolchain can create its normal compilation cache.
It does not change Nexal configuration, access credentials, or send telemetry.

## Interpret the result

- `vmMaximumAllowedMemoryBytes` is Apple's reported configuration ceiling,
  not the amount of available RAM.
- A true `proposal100GBWithinAPIRange` or `proposal100GiBWithinAPIRange`
  is only a range comparison. It does not validate a complete VM, prove that
  a guest boots, or provide remote backing.
- `hasUnifiedMemory` describes the Metal device's memory model, not a promise
  of 100 GB of GPU-resident memory.
- `recommendedMaxWorkingSetSizeBytes` and `maxBufferLengthBytes` are reported
  limits/guidance, not successful allocation or execution tests.
- Running inside a guest, if a guest already exists, produces a guest-context
  report. Label host and guest output separately; do not create a huge VM just
  to run this script.

Apple documents the relevant [VM memory maximum](https://developer.apple.com/documentation/virtualization/vzvirtualmachineconfiguration/maximumallowedmemorysize)
and [Metal unified-memory property](https://developer.apple.com/documentation/metal/mtldevice/hasunifiedmemory).
The full feasibility report is in the sibling private platform repository:
[CXL and remotely backed macOS VM memory](https://github.com/keithknott26/nexal-platform/blob/main/docs/CXL-AND-REMOTE-MACOS-MEMORY.md).
