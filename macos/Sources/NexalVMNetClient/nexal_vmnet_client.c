#include "nexal_vmnet_client.h"

#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/uio.h>
#include <sys/un.h>
#include <unistd.h>

static void set_info(char *info, size_t len, const char *what, const char *detail) {
    if (info && len) snprintf(info, len, "%s%s%s", what, detail ? ": " : "", detail ? detail : "");
}

int nexal_vmnet_attach(const char *socket_path, const char *interface_id, int *vm_fd, char *info, size_t info_len) {
    int sv[2] = {-1, -1};
    int cfd = -1;
    if (info && info_len) info[0] = '\0';
    if (vm_fd) *vm_fd = -1;
    if (!socket_path || !vm_fd) {
        set_info(info, info_len, "invalid arguments", NULL);
        return -1;
    }
    struct sockaddr_un addr;
    memset(&addr, 0, sizeof addr);
    addr.sun_family = AF_UNIX;
    if (strlen(socket_path) >= sizeof addr.sun_path) {
        set_info(info, info_len, "LAN bridge socket path too long", NULL);
        return -1;
    }
    strncpy(addr.sun_path, socket_path, sizeof addr.sun_path - 1);

    if (socketpair(AF_UNIX, SOCK_DGRAM, 0, sv) != 0) {
        set_info(info, info_len, "socketpair", strerror(errno));
        return -1;
    }
    // Lima's sizing for VZFileHandleNetworkDeviceAttachment: receive >= 2x send.
    int snd = 1 << 20, rcv = 4 << 20;
    for (int i = 0; i < 2; i++) {
        setsockopt(sv[i], SOL_SOCKET, SO_SNDBUF, &snd, sizeof snd);
        setsockopt(sv[i], SOL_SOCKET, SO_RCVBUF, &rcv, sizeof rcv);
    }
    cfd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (cfd < 0) {
        set_info(info, info_len, "socket", strerror(errno));
        goto fail;
    }
#ifdef SO_NOSIGPIPE
    int one = 1;
    setsockopt(cfd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#endif
    if (connect(cfd, (struct sockaddr *)&addr, sizeof addr) != 0) {
        char what[160];
        snprintf(what, sizeof what, "the LAN bridge helper is not reachable at %s", socket_path);
        set_info(info, info_len, what, strerror(errno));
        goto fail;
    }
    struct timeval tv = {.tv_sec = 20, .tv_usec = 0};
    setsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);

    char line[128];
    int n = snprintf(line, sizeof line, "NEXAL-VMNET 1 %s\n", (interface_id && *interface_id) ? interface_id : "-");
    if (n <= 0 || n >= (int)sizeof line) {
        set_info(info, info_len, "interface id too long", NULL);
        goto fail;
    }
    struct iovec iov = {.iov_base = line, .iov_len = (size_t)n};
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
    struct cmsghdr *h = CMSG_FIRSTHDR(&msg);
    h->cmsg_level = SOL_SOCKET;
    h->cmsg_type = SCM_RIGHTS;
    h->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(h), &sv[1], sizeof(int));
    if (sendmsg(cfd, &msg, 0) != n) {
        set_info(info, info_len, "sending to the LAN bridge helper failed", strerror(errno));
        goto fail;
    }
    close(sv[1]);  // the helper holds its own copy now
    sv[1] = -1;

    char resp[256];
    size_t got = 0;
    while (got < sizeof resp - 1) {
        ssize_t r = recv(cfd, resp + got, 1, 0);
        if (r <= 0 || resp[got] == '\n') break;
        got++;
    }
    resp[got] = '\0';
    if (strncmp(resp, "OK ", 3) == 0) {
        set_info(info, info_len, resp + 3, NULL);
        *vm_fd = sv[0];
        return cfd;
    }
    if (got == 0) {
        set_info(info, info_len, "no answer from the LAN bridge helper", NULL);
    } else {
        set_info(info, info_len, strncmp(resp, "ERR ", 4) == 0 ? resp + 4 : resp, NULL);
    }
fail:
    if (sv[0] >= 0) close(sv[0]);
    if (sv[1] >= 0) close(sv[1]);
    if (cfd >= 0) close(cfd);
    return -1;
}
