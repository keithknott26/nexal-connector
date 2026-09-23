package client

import (
	"context"
	"errors"
	"fmt"
)

// AccessRule is a directional, default-deny private-mesh rule. Forwarding, when
// present, binds on the source computer's mesh interface only.
type AccessRule struct {
	ID                   string  `json:"id"`
	NetworkID            string  `json:"networkId"`
	SourceComputerID     string  `json:"sourceComputerId"`
	TargetComputerID     string  `json:"targetComputerId"`
	Protocol             string  `json:"protocol"`
	PortStart            int     `json:"portStart"`
	PortEnd              int     `json:"portEnd"`
	Action               string  `json:"action"`
	Enabled              bool    `json:"enabled"`
	ForwardingComputerID *string `json:"forwardingComputerId"`
	ListenPort           *int    `json:"listenPort"`
	ForwardTargetPort    *int    `json:"forwardTargetPort"`
	Revision             int     `json:"revision"`
}
type AccessRuleSet struct {
	DefaultAction string       `json:"defaultAction"`
	DeviceID      string       `json:"deviceId"`
	NetworkID     string       `json:"networkId"`
	Rules         []AccessRule `json:"rules"`
}
type accessRuleApply struct {
	RuleID     string `json:"ruleId"`
	Revision   int    `json:"revision"`
	State      string `json:"state"`
	DetailCode string `json:"detailCode,omitempty"`
}

func validateRule(r AccessRule) error {
	if !ValidID(r.ID) || !ValidID(r.NetworkID) || !ValidID(r.SourceComputerID) || !ValidID(r.TargetComputerID) || r.SourceComputerID == r.TargetComputerID ||
		(r.Protocol != "tcp" && r.Protocol != "udp") || r.PortStart < 1 || r.PortEnd < r.PortStart || r.PortEnd > 65535 || (r.Action != "allow" && r.Action != "deny") || r.Revision < 1 || !r.Enabled {
		return errors.New("invalid access rule")
	}
	forwarding := r.ForwardingComputerID != nil || r.ListenPort != nil || r.ForwardTargetPort != nil
	if forwarding && (r.ForwardingComputerID == nil || r.ListenPort == nil || r.ForwardTargetPort == nil || *r.ForwardingComputerID != r.SourceComputerID || *r.ListenPort < 1 || *r.ListenPort > 65535 || *r.ForwardTargetPort < 1 || *r.ForwardTargetPort > 65535 || r.Action != "allow") {
		return errors.New("invalid forwarding rule")
	}
	return nil
}

func (c *Client) AccessRules(ctx context.Context, hostID string) (AccessRuleSet, error) {
	var out AccessRuleSet
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	if err := c.call(ctx, "GET", fmt.Sprintf("/api/v2/devices/%s/access-rules", hostID), nil, &out); err != nil {
		return out, err
	}
	if out.DefaultAction != "deny" || !ValidID(out.DeviceID) || !ValidID(out.NetworkID) {
		return out, errors.New("invalid access rule set")
	}
	for _, r := range out.Rules {
		if err := validateRule(r); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (c *Client) AcknowledgeAccessRule(ctx context.Context, hostID string, rule AccessRule, state, detail string) error {
	if !ValidID(hostID) || validateRule(rule) != nil || (state != "applied" && state != "failed") || len(detail) > 80 {
		return errors.New("invalid access rule acknowledgement")
	}
	var out struct {
		Acknowledged bool   `json:"acknowledged"`
		RuleID       string `json:"ruleId"`
		Revision     int    `json:"revision"`
		State        string `json:"state"`
	}
	if err := c.call(ctx, "POST", fmt.Sprintf("/api/v2/devices/%s/access-rules", hostID), accessRuleApply{rule.ID, rule.Revision, state, detail}, &out); err != nil {
		return err
	}
	if !out.Acknowledged || out.RuleID != rule.ID || out.Revision != rule.Revision || out.State != state {
		return errors.New("invalid access rule acknowledgement")
	}
	return nil
}
