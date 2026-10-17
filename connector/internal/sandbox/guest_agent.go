package sandbox

import "strings"

// guestAgentPath is where the seed installs the in-guest half of the guest channel (guest.go).
// Stock cloud images ship no neXal guest agent, so without this the host could never deliver the
// one-time VNC password and every live view stayed on "still preparing the password".
const guestAgentPath = "/usr/local/sbin/nexal-guest-agent"

// guestAgentPy reads one JSON request per line from the virtio console port /dev/hvc1 and answers
// one JSON line. It runs ONLY the allow-listed operations of guest.go; everything else is refused.
const guestAgentPy = `#!/usr/bin/env python3
import json, os, pwd, re, subprocess, termios, time, tty

DEV = "/dev/hvc1"
SECRET = re.compile(r"^[A-Za-z0-9._~+=-]{1,256}$")
VNC_DIR = "/home/nexal/.vnc"


def set_vnc_password(pw):
    if not SECRET.match(pw or ""):
        return {"ok": False, "error": "invalid password"}
    if not os.path.isdir(VNC_DIR) or not os.path.exists("/usr/bin/vncpasswd"):
        return {"ok": False, "error": "desktop not ready"}
    try:
        r = subprocess.run(["/usr/bin/vncpasswd", "-f"], input=pw.encode(), capture_output=True, timeout=10)
        if r.returncode != 0:
            return {"ok": False, "error": ("vncpasswd exit %d: " % r.returncode) + r.stderr.decode("utf-8", "replace").replace(pw, "***")[:120]}
        out = r.stdout
        path = VNC_DIR + "/passwd"
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "wb") as f:
            f.write(out)
        u = pwd.getpwnam("nexal")
        os.chown(path, u.pw_uid, u.pw_gid)
        return {"ok": True}
    except Exception as e:
        return {"ok": False, "error": ("could not set the password: %s %s" % (type(e).__name__, e)).replace(pw, "***")[:150]}


def handle(line):
    try:
        req = json.loads(line)
    except Exception:
        return {"ok": False, "error": "invalid request"}
    if not isinstance(req, dict):
        return {"ok": False, "error": "invalid request"}
    if req.get("op") == "vnc-password":
        return set_vnc_password(req.get("password"))
    return {"ok": False, "error": "unsupported"}


def serve():
    fd = os.open(DEV, os.O_RDWR | os.O_NOCTTY)
    try:
        tty.setraw(fd)
        attrs = termios.tcgetattr(fd)
        attrs[3] &= ~termios.ECHO
        termios.tcsetattr(fd, termios.TCSANOW, attrs)
    except Exception:
        pass
    buf = b""
    while True:
        chunk = os.read(fd, 4096)
        if not chunk:
            time.sleep(1)
            continue
        buf += chunk
        if len(buf) > 16384:
            buf = b""
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            if line.strip():
                os.write(fd, (json.dumps(handle(line.decode("utf-8", "replace"))) + "\n").encode())


while True:
    try:
        serve()
    except Exception:
        time.sleep(2)
`

const guestAgentUnit = `[Unit]
Description=neXal guest agent
After=local-fs.target
[Service]
ExecStart=` + guestAgentPath + `
Restart=always
RestartSec=2
[Install]
WantedBy=multi-user.target
`

// renderGuestAgent adds the guest agent and its unit to write_files.
func renderGuestAgent(b *strings.Builder) {
	b.WriteString("  - path: " + guestAgentPath + "\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0755\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, guestAgentPy)
	b.WriteString("  - path: /etc/systemd/system/nexal-guest-agent.service\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, guestAgentUnit)
}
