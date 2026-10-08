#ifndef NEXAL_VMNET_CLIENT_H
#define NEXAL_VMNET_CLIENT_H

#include <stddef.h>

/// Connects to the nexal-vmnet bridge helper at `socket_path`, hands it one end
/// of a fresh AF_UNIX SOCK_DGRAM socketpair and asks it to bridge that onto the
/// Mac's LAN. `interface_id` (a UUID, or NULL) keeps the vmnet interface stable.
///
/// On success returns the control socket -- keep it open for the VM's lifetime;
/// closing it ends the bridge -- and stores the other socketpair end, ready for
/// VZFileHandleNetworkDeviceAttachment, in *vm_fd; `info` gets
/// "<interface> <mtu>". On failure returns -1 and `info` gets a reason.
int nexal_vmnet_attach(const char *socket_path, const char *interface_id, int *vm_fd, char *info, size_t info_len);

#endif
