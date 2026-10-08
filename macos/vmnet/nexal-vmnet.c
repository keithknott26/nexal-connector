// nexal-vmnet: bridges neXal VMs onto this Mac's LAN, the way socket_vmnet does
// for Lima.
//
// Virtualization.framework can only bridge a VM onto the LAN with Apple's
// restricted com.apple.vm.networking entitlement. vmnet.framework's bridged mode
// needs root instead, so this small root daemon owns the vmnet interfaces and
// relays Ethernet frames to the VM host processes, which attach them with
// VZFileHandleNetworkDeviceAttachment (exactly how Lima's vz driver uses
// socket_vmnet).
//
// Protocol, one connection per VM, on a 0600 unix socket owned by the one user
// it serves (--uid; also checked per connection with getpeereid):
//
//   client -> "NEXAL-VMNET 1 <interface-uuid|->\n" with SCM_RIGHTS carrying one
//             end of an AF_UNIX SOCK_DGRAM socketpair
//   helper -> "OK <bridged-interface> <mtu>\n" | "ERR <reason>\n"
//
// From then on every datagram on that socket is one Ethernet frame, in both
// directions. Each VM gets its own vmnet interface, which lives exactly as long
// as the control connection: when the VM host exits, the bridge goes away.
//
// Usage: nexal-vmnet --uid <uid> [--socket /var/run/nexal-vmnet.sock] [--interface en0]
// Build: clang -O2 -o nexal-vmnet nexal-vmnet.c -framework vmnet \
//          -framework SystemConfiguration -framework CoreFoundation

#include <CoreFoundation/CoreFoundation.h>
#include <SystemConfiguration/SystemConfiguration.h>
#include <dispatch/dispatch.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/uio.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>
#include <uuid/uuid.h>
#include <vmnet/vmnet.h>
#include <xpc/xpc.h>

#define VERSION "0.1.0"
#define DEFAULT_SOCKET "/var/run/nexal-vmnet.sock"
#define MAX_BATCH 32
#define START_TIMEOUT_SEC 15

static uid_t g_uid;
static const char *g_iface_forced;

static void say(const char *fmt, ...) {
    char ts[32];
    time_t now = time(NULL);
    struct tm tm;
    localtime_r(&now, &tm);
    strftime(ts, sizeof ts, "%Y-%m-%dT%H:%M:%S", &tm);
    fprintf(stderr, "%s nexal-vmnet: ", ts);
    va_list ap;
    va_start(ap, fmt);
    vfprintf(stderr, fmt, ap);
    va_end(ap);
    fputc('\n', stderr);
    fflush(stderr);
}

// ---------------------------------------------------------------- interfaces

// The interface carrying the default route (Wi-Fi or Ethernet), from the
// dynamic store; re-read for every VM so a Mac that moved from Wi-Fi to
// Ethernet bridges the new one.
static int primary_interface(char *out, size_t len) {
    int ok = 0;
    SCDynamicStoreRef store = SCDynamicStoreCreate(NULL, CFSTR("nexal-vmnet"), NULL, NULL);
    if (!store) return 0;
    CFPropertyListRef v = SCDynamicStoreCopyValue(store, CFSTR("State:/Network/Global/IPv4"));
    if (v && CFGetTypeID(v) == CFDictionaryGetTypeID()) {
        CFTypeRef pi = CFDictionaryGetValue((CFDictionaryRef)v, CFSTR("PrimaryInterface"));
        if (pi && CFGetTypeID(pi) == CFStringGetTypeID() &&
            CFStringGetCString((CFStringRef)pi, out, (CFIndex)len, kCFStringEncodingUTF8)) {
            ok = 1;
        }
    }
    if (v) CFRelease(v);
    CFRelease(store);
    return ok;
}

// Picks the interface to bridge: --interface if given, else the primary one when
// vmnet can bridge it, else the first interface vmnet can bridge (the primary
// one may be a VPN tunnel).
static int pick_interface(char *out, size_t len, char *why, size_t wlen) {
    xpc_object_t list = vmnet_copy_shared_interface_list();
    size_t n = list ? xpc_array_get_count(list) : 0;
    char want[64] = "";
    if (g_iface_forced) {
        snprintf(want, sizeof want, "%s", g_iface_forced);
    } else if (!primary_interface(want, sizeof want)) {
        want[0] = '\0';
    }
    int found = 0;
    for (size_t i = 0; i < n && want[0]; i++) {
        const char *s = xpc_array_get_string(list, i);
        if (s && strcmp(s, want) == 0) {
            found = 1;
            break;
        }
    }
    if (found) {
        snprintf(out, len, "%s", want);
    } else if (g_iface_forced) {
        snprintf(why, wlen, "interface %s cannot be bridged", g_iface_forced);
    } else if (n > 0 && xpc_array_get_string(list, 0)) {
        snprintf(out, len, "%s", xpc_array_get_string(list, 0));
        found = 1;
    } else {
        snprintf(why, wlen, "this Mac has no network interface that can be bridged (is it on Wi-Fi or Ethernet?)");
    }
    if (list) xpc_release(list);
    return found;
}

// ------------------------------------------------------------------- bridges

typedef struct bridge {
    int cfd;                 // control connection: its EOF ends the bridge
    int vfd;                 // datagram socket to the VM host: one frame per datagram
    interface_ref iface;     // the vmnet interface (bridged)
    dispatch_queue_t q;      // serialises everything below
    dispatch_source_t vsrc;  // vfd readable
    dispatch_source_t csrc;  // cfd readable (EOF)
    size_t max_pkt;
    uint8_t *rbuf;                // one frame from the VM
    uint8_t *bufs[MAX_BATCH];     // frames from the LAN
    int closed;
    int refs;                // cancel handlers still to run before free
    char name[96];
} bridge_t;

static void bridge_release(bridge_t *b) {
    if (--b->refs > 0) return;
    free(b->rbuf);
    for (int i = 0; i < MAX_BATCH; i++) free(b->bufs[i]);
    dispatch_release(b->q);
    free(b);
}

// Tears the bridge down (on b->q). Idempotent.
static void bridge_close(bridge_t *b, const char *why) {
    if (b->closed) return;
    b->closed = 1;
    say("bridge closed: %s (%s)", b->name, why);
    if (b->iface) {
        interface_ref iface = b->iface;
        b->iface = NULL;
        vmnet_interface_set_event_callback(iface, VMNET_INTERFACE_PACKETS_AVAILABLE, NULL, NULL);
        vmnet_stop_interface(iface, b->q, ^(vmnet_return_t status) {
            if (status != VMNET_SUCCESS) say("vmnet_stop_interface: status %d", (int)status);
        });
    }
    dispatch_source_cancel(b->vsrc);
    dispatch_source_cancel(b->csrc);
}

// VM -> LAN: each datagram is one frame.
static void on_vm_readable(bridge_t *b) {
    for (int i = 0; i < 256 && !b->closed; i++) {
        ssize_t n = recv(b->vfd, b->rbuf, b->max_pkt, MSG_DONTWAIT);
        if (n < 0) {
            if (errno == EINTR) continue;
            if (errno == EAGAIN || errno == EWOULDBLOCK) return;
            bridge_close(b, strerror(errno));
            return;
        }
        if (n == 0) continue;
        struct iovec iov = {.iov_base = b->rbuf, .iov_len = (size_t)n};
        struct vmpktdesc pd = {.vm_pkt_size = (size_t)n, .vm_pkt_iov = &iov, .vm_pkt_iovcnt = 1, .vm_flags = 0};
        int cnt = 1;
        vmnet_return_t r = vmnet_write(b->iface, &pd, &cnt);
        if (r != VMNET_SUCCESS && r != VMNET_BUFFER_EXHAUSTED) {
            say("vmnet_write: status %d (%s)", (int)r, b->name);
        }
    }
}

// LAN -> VM: drain vmnet in batches. A full VM socket buffer drops the frame
// (as a switch would); a VM host that is gone ends the bridge.
static void on_lan_packets(bridge_t *b) {
    struct vmpktdesc pd[MAX_BATCH];
    struct iovec iov[MAX_BATCH];
    for (;;) {
        if (b->closed || !b->iface) return;
        for (int i = 0; i < MAX_BATCH; i++) {
            iov[i].iov_base = b->bufs[i];
            iov[i].iov_len = b->max_pkt;
            pd[i].vm_pkt_size = b->max_pkt;
            pd[i].vm_pkt_iov = &iov[i];
            pd[i].vm_pkt_iovcnt = 1;
            pd[i].vm_flags = 0;
        }
        int cnt = MAX_BATCH;
        vmnet_return_t r = vmnet_read(b->iface, pd, &cnt);
        if (r != VMNET_SUCCESS || cnt <= 0) return;
        for (int i = 0; i < cnt; i++) {
            if (send(b->vfd, b->bufs[i], pd[i].vm_pkt_size, MSG_DONTWAIT) < 0) {
                if (errno == ECONNREFUSED || errno == ENOTCONN || errno == EPIPE || errno == EBADF) {
                    bridge_close(b, "the VM host is gone");
                    return;
                }
                // EAGAIN / ENOBUFS: the VM is not keeping up; drop this frame.
            }
        }
        if (cnt < MAX_BATCH) return;
    }
}

static void reply(int fd, const char *fmt, ...) {
    char line[256];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(line, sizeof line - 1, fmt, ap);
    va_end(ap);
    if (n < 0) return;
    if (n > (int)sizeof line - 2) n = (int)sizeof line - 2;
    line[n++] = '\n';
    (void)send(fd, line, (size_t)n, 0);
}

// Handshake and setup for one VM. Runs on a global queue (it blocks waiting for
// vmnet); the bridge itself then lives on its own serial queue.
static void handle_client(int cfd) {
    uid_t euid = (uid_t)-1;
    gid_t egid = (gid_t)-1;
    if (getpeereid(cfd, &euid, &egid) != 0 || (euid != g_uid && euid != 0)) {
        say("refused a connection from uid %d", (int)euid);
        reply(cfd, "ERR not allowed");
        close(cfd);
        return;
    }
    struct timeval tv = {.tv_sec = 10, .tv_usec = 0};
    setsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);

    char line[160];
    memset(line, 0, sizeof line);
    struct iovec iov = {.iov_base = line, .iov_len = sizeof line - 1};
    union {
        struct cmsghdr hdr;
        char buf[CMSG_SPACE(sizeof(int))];
    } cm;
    memset(&cm, 0, sizeof cm);
    struct msghdr msg;
    memset(&msg, 0, sizeof msg);
    msg.msg_iov = &iov;
    msg.msg_iovlen = 1;
    msg.msg_control = cm.buf;
    msg.msg_controllen = sizeof cm.buf;
    ssize_t n = recvmsg(cfd, &msg, 0);
    int vfd = -1;
    if (n > 0) {
        for (struct cmsghdr *h = CMSG_FIRSTHDR(&msg); h; h = CMSG_NXTHDR(&msg, h)) {
            if (h->cmsg_level == SOL_SOCKET && h->cmsg_type == SCM_RIGHTS && h->cmsg_len >= CMSG_LEN(sizeof(int))) {
                memcpy(&vfd, CMSG_DATA(h), sizeof(int));
            }
        }
    }
    char id[64] = "";
    if (n <= 0 || vfd < 0 || sscanf(line, "NEXAL-VMNET 1 %63s", id) != 1) {
        reply(cfd, "ERR bad request (expected NEXAL-VMNET 1 <id> with a datagram socket)");
        if (vfd >= 0) close(vfd);
        close(cfd);
        return;
    }
    int type = 0;
    socklen_t tl = sizeof type;
    if (getsockopt(vfd, SOL_SOCKET, SO_TYPE, &type, &tl) != 0 || type != SOCK_DGRAM) {
        reply(cfd, "ERR the passed descriptor is not a datagram socket");
        close(vfd);
        close(cfd);
        return;
    }
    int snd = 1 << 20, rcv = 4 << 20;  // receive at least twice the send buffer, as Lima does
    setsockopt(vfd, SOL_SOCKET, SO_SNDBUF, &snd, sizeof snd);
    setsockopt(vfd, SOL_SOCKET, SO_RCVBUF, &rcv, sizeof rcv);
    int one = 1;
    setsockopt(vfd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
    fcntl(vfd, F_SETFL, fcntl(vfd, F_GETFL) | O_NONBLOCK);

    char ifname[64], why[160] = "";
    if (!pick_interface(ifname, sizeof ifname, why, sizeof why)) {
        say("cannot bridge for %s: %s", id, why);
        reply(cfd, "ERR %s", why);
        close(vfd);
        close(cfd);
        return;
    }

    bridge_t *b = calloc(1, sizeof *b);
    if (!b) {
        reply(cfd, "ERR out of memory");
        close(vfd);
        close(cfd);
        return;
    }
    b->cfd = cfd;
    b->vfd = vfd;
    snprintf(b->name, sizeof b->name, "%s on %s", id, ifname);
    b->q = dispatch_queue_create("systems.nexal.vmnet.bridge", DISPATCH_QUEUE_SERIAL);

    xpc_object_t desc = xpc_dictionary_create(NULL, NULL, 0);
    xpc_dictionary_set_uint64(desc, vmnet_operation_mode_key, VMNET_BRIDGED_MODE);
    xpc_dictionary_set_string(desc, vmnet_shared_interface_name_key, ifname);
    uuid_t uu;
    if (uuid_parse(id, uu) == 0) xpc_dictionary_set_uuid(desc, vmnet_interface_id_key, uu);

    __block vmnet_return_t status = VMNET_FAILURE;
    __block uint64_t mtu = 1500, max_pkt = 1514;
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    interface_ref iface = vmnet_start_interface(desc, b->q, ^(vmnet_return_t st, xpc_object_t params) {
        status = st;
        if (st == VMNET_SUCCESS && params) {
            mtu = xpc_dictionary_get_uint64(params, vmnet_mtu_key);
            max_pkt = xpc_dictionary_get_uint64(params, vmnet_max_packet_size_key);
        }
        dispatch_semaphore_signal(done);
    });
    xpc_release(desc);
    long timed_out = iface ? dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, (int64_t)START_TIMEOUT_SEC * NSEC_PER_SEC)) : 1;
    dispatch_release(done);
    if (!iface || timed_out || status != VMNET_SUCCESS) {
        say("vmnet_start_interface failed for %s (status %d%s)", b->name, (int)status, timed_out ? ", timed out" : "");
        reply(cfd, "ERR could not bridge onto %s (vmnet status %d)", ifname, (int)status);
        if (iface && !timed_out) vmnet_stop_interface(iface, b->q, ^(vmnet_return_t st) { (void)st; });
        dispatch_release(b->q);
        free(b);
        close(vfd);
        close(cfd);
        return;
    }
    if (max_pkt < 1514 || max_pkt > 65536) max_pkt = 1514;
    b->iface = iface;
    b->max_pkt = (size_t)max_pkt;
    b->rbuf = malloc(b->max_pkt);
    int oom = !b->rbuf;
    for (int i = 0; i < MAX_BATCH; i++) {
        b->bufs[i] = malloc(b->max_pkt);
        if (!b->bufs[i]) oom = 1;
    }
    if (oom) {
        reply(cfd, "ERR out of memory");
        vmnet_stop_interface(iface, b->q, ^(vmnet_return_t st) { (void)st; });
        free(b->rbuf);
        for (int i = 0; i < MAX_BATCH; i++) free(b->bufs[i]);
        dispatch_release(b->q);
        free(b);
        close(vfd);
        close(cfd);
        return;
    }

    b->refs = 2;
    b->vsrc = dispatch_source_create(DISPATCH_SOURCE_TYPE_READ, (uintptr_t)vfd, 0, b->q);
    b->csrc = dispatch_source_create(DISPATCH_SOURCE_TYPE_READ, (uintptr_t)cfd, 0, b->q);
    dispatch_source_set_event_handler(b->vsrc, ^{ on_vm_readable(b); });
    dispatch_source_set_cancel_handler(b->vsrc, ^{
        close(b->vfd);
        dispatch_release(b->vsrc);
        bridge_release(b);
    });
    dispatch_source_set_event_handler(b->csrc, ^{
        char tmp[64];
        ssize_t r = recv(b->cfd, tmp, sizeof tmp, MSG_DONTWAIT);
        if (r == 0 || (r < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR)) {
            bridge_close(b, "the VM host disconnected");
        }
    });
    dispatch_source_set_cancel_handler(b->csrc, ^{
        close(b->cfd);
        dispatch_release(b->csrc);
        bridge_release(b);
    });

    const char *ifn = ifname;  // blocks cannot capture arrays; dispatch_sync keeps it alive
    dispatch_sync(b->q, ^{
        vmnet_interface_set_event_callback(b->iface, VMNET_INTERFACE_PACKETS_AVAILABLE, b->q,
                                           ^(interface_event_t mask, xpc_object_t event) {
                                               (void)mask;
                                               (void)event;
                                               on_lan_packets(b);
                                           });
        reply(cfd, "OK %s %llu", ifn, (unsigned long long)mtu);
        say("bridge up: %s (mtu %llu, frames up to %zu bytes)", b->name, (unsigned long long)mtu, b->max_pkt);
        dispatch_resume(b->vsrc);
        dispatch_resume(b->csrc);
    });
}

// ---------------------------------------------------------------------- main

static void usage(void) {
    fprintf(stderr, "usage: nexal-vmnet --uid <uid> [--socket %s] [--interface <name>] | --version\n", DEFAULT_SOCKET);
    exit(2);
}

int main(int argc, char **argv) {
    const char *sock_path = DEFAULT_SOCKET;
    long uid = -1;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--version") == 0) {
            printf("nexal-vmnet %s\n", VERSION);
            return 0;
        } else if (strcmp(argv[i], "--socket") == 0 && i + 1 < argc) {
            sock_path = argv[++i];
        } else if (strcmp(argv[i], "--uid") == 0 && i + 1 < argc) {
            char *end = NULL;
            uid = strtol(argv[++i], &end, 10);
            if (!end || *end) usage();
        } else if (strcmp(argv[i], "--interface") == 0 && i + 1 < argc) {
            g_iface_forced = argv[++i];
        } else {
            usage();
        }
    }
    if (uid <= 0) usage();  // never serve root-only or everyone
    if (geteuid() != 0) {
        say("must run as root (vmnet bridged mode needs it)");
        return 2;
    }
    g_uid = (uid_t)uid;
    signal(SIGPIPE, SIG_IGN);

    struct sockaddr_un addr;
    memset(&addr, 0, sizeof addr);
    addr.sun_family = AF_UNIX;
    if (strlen(sock_path) >= sizeof addr.sun_path) {
        say("socket path too long: %s", sock_path);
        return 2;
    }
    strncpy(addr.sun_path, sock_path, sizeof addr.sun_path - 1);
    addr.sun_len = (unsigned char)SUN_LEN(&addr);

    int lfd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (lfd < 0) {
        say("socket: %s", strerror(errno));
        return 1;
    }
    unlink(sock_path);
    mode_t old = umask(0177);
    if (bind(lfd, (struct sockaddr *)&addr, sizeof addr) != 0) {
        say("bind %s: %s", sock_path, strerror(errno));
        return 1;
    }
    umask(old);
    // Only the served user (and root) may connect; getpeereid re-checks.
    if (chown(sock_path, g_uid, (gid_t)-1) != 0 || chmod(sock_path, 0600) != 0) {
        say("cannot hand %s to uid %d: %s", sock_path, (int)g_uid, strerror(errno));
        return 1;
    }
    if (listen(lfd, 16) != 0) {
        say("listen: %s", strerror(errno));
        return 1;
    }
    say("%s listening on %s for uid %d", VERSION, sock_path, (int)g_uid);

    for (;;) {
        int cfd = accept(lfd, NULL, NULL);
        if (cfd < 0) {
            if (errno == EINTR || errno == ECONNABORTED) continue;
            say("accept: %s", strerror(errno));
            sleep(1);
            continue;
        }
        int one = 1;
        setsockopt(cfd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{ handle_client(cfd); });
    }
}
