package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Guest channel: how the runner talks to a running VM after first boot.
//
// Chosen option: a unix socket on the Mac that nexal-vmhost bridges to a second
// virtio console port (/dev/hvc1 in the guest). It needs no guest networking and
// works before the mesh is up, which is what a rejoin needs. The protocol is one
// JSON object per line in each direction:
//
//	request:  {"op":"vnc-password","password":"<pw>"}
//	          {"op":"mesh-rejoin","key":"<one-use setup key>"}
//	          {"op":"mesh-status"}
//	reply:    {"ok":true,"connected":true,"meshIp":"100.64.1.7"}
//	          {"ok":false,"error":"<short reason>"}
//
// In the guest a small root service (nexal-guest-agent, part of the neXal-ready
// image) reads the port and runs ONLY these allow-listed operations:
// `nexal-vnc-password set <pw>`, the mesh runtime's re-registration with the
// given key, and a mesh status check. Nothing else is executable through it.
const (
	GuestOpVNCPassword = "vnc-password"
	GuestOpMeshRejoin  = "mesh-rejoin"
	GuestOpMeshStatus  = "mesh-status"
)

// GuestCommand is one request to the guest agent. It carries secrets: it must
// never be logged (String redacts).
type GuestCommand struct {
	Op       string `json:"op"`
	Password string `json:"password,omitempty"`
	Key      string `json:"key,omitempty"`
}

// String implements fmt.Stringer without any secret.
func (c GuestCommand) String() string { return "sandbox.GuestCommand{op=" + c.Op + "}" }

// GuestReply is the guest agent's answer.
type GuestReply struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	MeshIP    string `json:"meshIp,omitempty"`
}

// ValidateGuestCommand checks the op and the values it interpolates.
func ValidateGuestCommand(c GuestCommand) error {
	switch c.Op {
	case GuestOpVNCPassword:
		if !validSecret(c.Password) {
			return errors.New("invalid vnc password")
		}
	case GuestOpMeshRejoin:
		if !validSecret(c.Key) {
			return errors.New("invalid setup key")
		}
	case GuestOpMeshStatus:
	default:
		return fmt.Errorf("unknown guest op %q", c.Op)
	}
	return nil
}

// GuestChannel sends commands to a running guest.
type GuestChannel interface {
	Send(ctx context.Context, h Handle, c GuestCommand) (GuestReply, error)
}

// SocketGuestChannel talks to the unix socket in Handle.Guest.
type SocketGuestChannel struct {
	Timeout time.Duration // per command; default 8 s
}

// Send implements GuestChannel.
func (s SocketGuestChannel) Send(ctx context.Context, h Handle, c GuestCommand) (GuestReply, error) {
	if err := ValidateGuestCommand(c); err != nil {
		return GuestReply{}, err
	}
	if h.Guest == "" {
		return GuestReply{}, errors.New("no guest channel for this sandbox")
	}
	to := s.Timeout
	if to <= 0 {
		to = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", h.Guest)
	if err != nil {
		return GuestReply{}, errors.New("guest channel unavailable")
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	return exchangeGuest(conn, c)
}

// exchangeGuest writes one request line and reads one reply line.
func exchangeGuest(conn net.Conn, c GuestCommand) (GuestReply, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return GuestReply{}, errors.New("cannot encode guest command")
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return GuestReply{}, errors.New("guest channel write failed")
	}
	r := bufio.NewReaderSize(conn, 8<<10)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return GuestReply{}, errors.New("guest channel closed without a reply")
	}
	var rep GuestReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return GuestReply{}, errors.New("guest channel sent an invalid reply")
	}
	if rep.MeshIP != "" {
		if _, err := netip.ParseAddr(rep.MeshIP); err != nil {
			rep.MeshIP = "" // the guest is untrusted
		}
	}
	if len(rep.Error) > 200 {
		rep.Error = rep.Error[:200]
	}
	if !rep.OK {
		if rep.Error == "" {
			rep.Error = "guest refused the command"
		}
		return rep, errors.New("guest: " + rep.Error)
	}
	return rep, nil
}
