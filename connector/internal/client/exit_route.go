package client

import (
	"context"
	"errors"
	"net/http"
	"nexal/connector/internal/observability"
	"regexp"
	"time"
)

var exitDeviceID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var exitRouteID = regexp.MustCompile(`^nx-exit-[a-f0-9]{32}$`)

func ValidExitDeviceID(value string) bool { return exitDeviceID.MatchString(value) }

type ExitRouteAck struct {
	SchemaVersion     int    `json:"schemaVersion"`
	NetworkID         string `json:"networkId"`
	SourceDeviceID    string `json:"sourceDeviceId"`
	TargetDeviceID    string `json:"targetDeviceId"`
	RouteID           string `json:"routeId"`
	Enabled           bool   `json:"enabled"`
	Configured        bool   `json:"configured"`
	SelectionRequired bool   `json:"selectionRequired"`
	AutoApply         bool   `json:"autoApply"`
	Readiness         string `json:"readiness"`
	IPv6              string `json:"ipv6"`
	Online            *bool  `json:"online"`
}

// ConfigureExitRoute changes only the authenticated source host's scoped route.
// It does not select the route locally or disable either computer's firewall.
func (c *Client) ConfigureExitRoute(ctx context.Context, hostID, tunnel, targetDeviceID string, enabled bool) (ExitRouteAck, error) {
	var ack ExitRouteAck
	if !ValidID(hostID) || len(hostID) > 80 {
		return ack, errors.New("invalid host id")
	}
	if enabled && targetDeviceID != "" {
		return ack, errors.New("target device is only valid when disabling an exit route")
	}
	body := struct {
		Enabled             bool   `json:"enabled"`
		TargetTunnelAddress string `json:"targetTunnelAddress,omitempty"`
		TargetDeviceID      string `json:"targetDeviceId,omitempty"`
	}{Enabled: enabled}
	if !enabled && targetDeviceID != "" {
		if !ValidExitDeviceID(targetDeviceID) {
			return ack, errors.New("invalid target device id")
		}
		// Stable identity deliberately replaces the address for revoked targets,
		// whose old tunnel-address lookup may no longer exist.
		body.TargetDeviceID = targetDeviceID
	} else {
		if !ValidTunnelAddress(tunnel) {
			return ack, errors.New("target must be a secure-network IPv4 address")
		}
		body.TargetTunnelAddress = tunnel
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Route provisioning can take longer than the ordinary ten-second request.
	// Clone the existing validated transport; preserve its origin/TLS/proxy policy.
	api := *c
	httpClient := *c.http
	httpClient.Timeout = 30 * time.Second
	if transport, ok := observability.Underlying(httpClient.Transport).(*http.Transport); ok {
		clone := transport.Clone()
		clone.ResponseHeaderTimeout = 30 * time.Second
		httpClient.Transport = observability.Transport{Base: clone}
		defer clone.CloseIdleConnections()
	}
	api.http = &httpClient
	if err := api.callLenient(ctx, "POST", "/api/hosts/"+hostID+"/exit-route", body, &ack); err != nil {
		return ExitRouteAck{}, err
	}
	readiness := "not_configured"
	if enabled {
		readiness = "awaiting_client_verification"
	}
	if ack.SchemaVersion != 1 || ack.Enabled != enabled || ack.Configured != enabled || ack.SelectionRequired != enabled || ack.AutoApply || ack.Readiness != readiness || ack.IPv6 != "provider_managed" || !exitRouteID.MatchString(ack.RouteID) || !ValidExitDeviceID(ack.SourceDeviceID) || !ValidExitDeviceID(ack.TargetDeviceID) || ack.SourceDeviceID == ack.TargetDeviceID || (targetDeviceID != "" && ack.TargetDeviceID != targetDeviceID) {
		return ExitRouteAck{}, errors.New("invalid exit route acknowledgement")
	}
	return ack, nil
}
