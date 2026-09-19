// Package runtimebridge builds fixed argv arrays for the minimal Python MLX
// adapter. It never runs a shell, SSH, installer, or network listener. Resource
// placement/reservations remain in connector/internal/pool, not a second scheduler.
package runtimebridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrGate = errors.New("runtime release gate closed")

type LocalInstallation struct {
	// All paths come from owner-managed local configuration, never a remote job.
	Python string `json:"python"`
	Entry  string `json:"entry"`
	Config string `json:"config"`
}

type Command struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	// A local supervisor must enforce this and terminate its process group.
	TimeoutSeconds int `json:"timeoutSeconds"`
}

func localPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsAny(path, "\x00\r\n") && len(path) <= 4096
}

func (i LocalInstallation) base() (Command, error) {
	if !localPath(i.Python) || !localPath(i.Entry) || !localPath(i.Config) ||
		filepath.Base(i.Entry) != "nexal_mlx_entry.py" {
		return Command{}, fmt.Errorf("%w: owner-pinned absolute installation paths required", ErrGate)
	}
	// Do not create mutable bytecode in the reviewed, read-only runtime tree.
	return Command{Executable: i.Python, Args: []string{"-I", "-B", i.Entry}, TimeoutSeconds: 35}, nil
}

func InferenceCommand(i LocalInstallation, admission, prompt string, maxTokens int) (Command, error) {
	command, err := i.base()
	if err != nil {
		return Command{}, err
	}
	if !localPath(admission) || !localPath(prompt) || maxTokens < 1 || maxTokens > 512 {
		return Command{}, fmt.Errorf("%w: input paths or generation limit invalid", ErrGate)
	}
	command.Args = append(command.Args, "infer", "--config", i.Config,
		"--admission", admission, "--prompt-file", prompt, "--max-tokens", strconv.Itoa(maxTokens))
	command.TimeoutSeconds = 300
	return command, nil
}

type Peer struct {
	DeviceID     string            `json:"deviceId"`
	Address      string            `json:"address"`
	Port         uint16            `json:"port"`
	Chip         string            `json:"chip"`
	Trusted      bool              `json:"trusted"`
	Installation LocalInstallation `json:"installation"`
	HostfilePath string            `json:"hostfilePath"`
}

type RankCommand struct {
	Rank     int     `json:"rank"`
	DeviceID string  `json:"deviceId"`
	Command  Command `json:"command"`
}

type RingSmokePlan struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Backend        string          `json:"backend"`
	Hostfile       json.RawMessage `json:"hostfile"`
	HostfileSHA256 string          `json:"hostfileSha256"`
	Ranks          []RankCommand   `json:"ranks"`
	// Even a successful scalar collective does not implement model sharding.
	DistributedInferenceEnabled bool `json:"distributedInferenceEnabled"`
}

var lanPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// PlanRingSmoke produces one local command per already-enrolled, explicitly
// approved peer. The owner copies the identical hostfile to each pinned path.
// It does not start SSH, change a firewall, open a listener, or launch commands.
// Model rank placement must first be done by pool.PlanMLX; this only verifies
// a scalar ring and must never be presented as distributed inference.
func PlanRingSmoke(backend string, peers []Peer, experimentalTrustedLAN bool) (RingSmokePlan, error) {
	if !experimentalTrustedLAN {
		return RingSmokePlan{}, fmt.Errorf("%w: explicit experimental trusted-LAN consent required", ErrGate)
	}
	if backend == "jaccl" {
		for _, peer := range peers {
			if strings.EqualFold(strings.TrimSpace(peer.Chip), "M4") ||
				strings.EqualFold(strings.TrimSpace(peer.Chip), "M2") {
				return RingSmokePlan{}, fmt.Errorf("%w: base M4/M2 are not JACCL Thunderbolt-5 peers", ErrGate)
			}
		}
		return RingSmokePlan{}, fmt.Errorf("%w: JACCL launch not validated in this runtime", ErrGate)
	}
	if backend != "ring" || len(peers) < 2 || len(peers) > 8 {
		return RingSmokePlan{}, fmt.Errorf("%w: only 2–8 rank ring smoke is supported", ErrGate)
	}
	hostfile := make([][]string, len(peers))
	seenIDs, seenIPs := map[string]bool{}, map[string]bool{}
	for rank, peer := range peers {
		ip, err := netip.ParseAddr(peer.Address)
		allowed := false
		for _, prefix := range lanPrefixes {
			allowed = allowed || prefix.Contains(ip)
		}
		id, idErr := hex.DecodeString(peer.DeviceID)
		if err != nil || !ip.Is4() || !allowed || peer.Port < 1024 || !peer.Trusted ||
			idErr != nil || len(id) != 32 || peer.DeviceID != strings.ToLower(peer.DeviceID) ||
			seenIDs[peer.DeviceID] || seenIPs[peer.Address] || !localPath(peer.HostfilePath) {
			return RingSmokePlan{}, fmt.Errorf("%w: peer identity/address/consent invalid", ErrGate)
		}
		seenIDs[peer.DeviceID], seenIPs[peer.Address] = true, true
		hostfile[rank] = []string{netip.AddrPortFrom(ip, peer.Port).String()}
	}
	bytes, err := json.Marshal(hostfile)
	if err != nil {
		return RingSmokePlan{}, err
	}
	hash := sha256.Sum256(bytes)
	plan := RingSmokePlan{SchemaVersion: 1, Backend: "ring", Hostfile: bytes,
		HostfileSHA256: hex.EncodeToString(hash[:]), Ranks: make([]RankCommand, 0, len(peers))}
	for rank, peer := range peers {
		command, err := peer.Installation.base()
		if err != nil {
			return RingSmokePlan{}, err
		}
		command.Args = append(command.Args, "ring-smoke", "--config", peer.Installation.Config,
			"--hostfile", peer.HostfilePath, "--hostfile-sha256", plan.HostfileSHA256,
			"--rank", strconv.Itoa(rank), "--experimental-trusted-lan")
		plan.Ranks = append(plan.Ranks, RankCommand{rank, peer.DeviceID, command})
	}
	return plan, nil
}
