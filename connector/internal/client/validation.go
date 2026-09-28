package client

import (
	"context"
	"errors"
	"nexal/connector/internal/cybersecurity"
	"time"
)

// ValidationTick asks for work only; absence of an enrolled lease never runs a test.
func (c *Client) ValidationTick(ctx context.Context, hostID string, canary cybersecurity.Canary, scanner cybersecurity.Scanner) error {
	if !ValidID(hostID) {
		return errors.New("invalid host")
	}
	base := "/api/hosts/" + hostID + "/security/validation"
	var response struct {
		SchemaVersion int                          `json:"schemaVersion"`
		Run           *cybersecurity.ValidationRun `json:"run"`
	}
	if err := c.call(ctx, "POST", base, nil, &response); err != nil {
		return err
	}
	if response.SchemaVersion != 1 {
		return errors.New("invalid validation response")
	}
	if response.Run == nil {
		return nil
	}
	run := *response.Run
	if err := run.Validate(time.Now()); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, run.ExpiresAt)
	defer cancel()
	authorized := func(ctx context.Context) bool {
		var grant struct {
			SchemaVersion int  `json:"schemaVersion"`
			Authorized    bool `json:"authorized"`
		}
		return c.call(ctx, "GET", base+"/"+run.ID, nil, &grant) == nil && grant.SchemaVersion == 1 && grant.Authorized
	}
	if !authorized(ctx) {
		return errors.New("validation revoked")
	}
	report := func(ctx context.Context, e cybersecurity.Event) error {
		if !authorized(ctx) {
			return errors.New("validation revoked")
		}
		return c.ReportSecurityEvent(ctx, hostID, e)
	}
	var executed bool
	var reason string
	var err error
	if run.Module == "network_canary" {
		ready := func(ctx context.Context) error {
			var ack struct {
				SchemaVersion int  `json:"schemaVersion"`
				Accepted      bool `json:"accepted"`
			}
			if err := c.call(ctx, "POST", base+"/"+run.ID+"/ready", nil, &ack); err != nil {
				return err
			}
			if ack.SchemaVersion != 1 || !ack.Accepted {
				return errors.New("not armed")
			}
			return nil
		}
		executed, reason, err = cybersecurity.ServeValidation(ctx, run, authorized, ready, report)
	} else {
		executed, reason, err = canary.ValidateLocal(ctx, scanner, run, report)
	}
	if err != nil {
		executed = false
		reason = "execution_failed"
	}
	var ack struct {
		SchemaVersion int  `json:"schemaVersion"`
		Accepted      bool `json:"accepted"`
	}
	body := struct {
		Executed bool   `json:"executed"`
		Reason   string `json:"reason"`
	}{executed, reason}
	if err := c.call(ctx, "POST", base+"/"+run.ID+"/complete", body, &ack); err != nil {
		return err
	}
	if ack.SchemaVersion != 1 || !ack.Accepted {
		return errors.New("invalid validation acknowledgement")
	}
	return nil
}
