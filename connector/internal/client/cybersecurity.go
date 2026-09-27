package client

import (
	"context"
	"errors"
	"nexal/connector/internal/cybersecurity"
	"time"
)

// ReportSecurityEvent uses the existing origin-validated, bounded HTTP transport.
// The caller persists the event before sending and removes it only after this
// acknowledgement. Retrying uses the same event ID and identical content.
func (c *Client) ReportSecurityEvent(ctx context.Context, hostID string, event cybersecurity.Event) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	if err := event.Validate(time.Now()); err != nil {
		return err
	}
	var ack struct {
		SchemaVersion int    `json:"schemaVersion"`
		Accepted      bool   `json:"accepted"`
		EventID       string `json:"eventId"`
	}
	if err := c.call(ctx, "POST", "/api/hosts/"+hostID+"/security-events", event, &ack); err != nil {
		return err
	}
	if ack.SchemaVersion != 1 || !ack.Accepted || ack.EventID != event.EventID {
		return errors.New("invalid security event acknowledgement")
	}
	return nil
}

// ReportWatermarkState sends metadata only. Marker values and paths stay local.
func (c *Client) ReportWatermarkState(ctx context.Context, hostID string, state cybersecurity.CanaryState) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	reportedAt := time.Now().UTC().Format(cybersecurity.TimeLayout)
	body := struct {
		SchemaVersion int    `json:"schemaVersion"`
		ReportedAt    string `json:"reportedAt"`
		Enabled       bool   `json:"enabled"`
		Status        string `json:"status"`
		LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	}{1, reportedAt, state.Enabled, state.Status, state.LastCheckedAt}
	var ack struct {
		SchemaVersion int    `json:"schemaVersion"`
		Accepted      bool   `json:"accepted"`
		ReportedAt    string `json:"reportedAt"`
	}
	if err := c.call(ctx, "POST", "/api/hosts/"+hostID+"/watermark-state", body, &ack); err != nil {
		return err
	}
	if ack.SchemaVersion != 1 || !ack.Accepted || ack.ReportedAt != reportedAt {
		return errors.New("invalid watermark acknowledgement")
	}
	return nil
}
