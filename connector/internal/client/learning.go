package client

import (
	"context"
	"errors"
	"net/http"
	"nexal/connector/internal/observability"
	"time"
)

// LearningTick orchestrates server-owned analysis of already uploaded evidence.
// No proprietary model code, provider key, raw file, or execution action lives here.
func (c *Client) LearningTick(ctx context.Context, hostID string) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	base := "/api/hosts/" + hostID + "/security/learning"
	var pending struct {
		SchemaVersion int      `json:"schemaVersion"`
		EventIDs      []string `json:"eventIds"`
	}
	if err := c.callLenient(ctx, "GET", base+"/events", nil, &pending); err != nil {
		return err
	}
	if pending.SchemaVersion != 1 || len(pending.EventIDs) > 8 {
		return errors.New("invalid learning queue")
	}
	seen := map[string]bool{}
	for _, id := range pending.EventIDs {
		if !ValidID(id) || seen[id] {
			return errors.New("invalid learning evidence")
		}
		seen[id] = true
	}
	if len(pending.EventIDs) == 0 {
		return nil
	}
	// Analysis can include two bounded model calls and one external review. Clone
	// transport rather than extending normal heartbeat request deadlines globally.
	copyClient := *c
	httpCopy := *c.http
	observed, ok := c.http.Transport.(observability.Transport)
	if !ok {
		return errors.New("unsupported learning transport")
	}
	transport, ok := observed.Base.(*http.Transport)
	if !ok {
		return errors.New("unsupported learning transport")
	}
	tr := transport.Clone()
	tr.ResponseHeaderTimeout = 305 * time.Second
	defer tr.CloseIdleConnections()
	httpCopy.Transport = observability.Transport{Base: tr}
	httpCopy.Timeout = 310 * time.Second
	copyClient.http = &httpCopy
	var ack struct {
		SchemaVersion               int    `json:"schemaVersion"`
		ID                          string `json:"id"`
		Status                      string `json:"status"`
		TrainingEligible            bool   `json:"trainingEligible"`
		AutomaticResponseAuthorized bool   `json:"automaticResponseAuthorized"`
	}
	if err := copyClient.callLenient(ctx, "POST", base+"/analyze", map[string]any{"eventIds": pending.EventIDs, "escalate": true, "challenge": true}, &ack); err != nil {
		return err
	}
	if ack.SchemaVersion != 1 || !ValidID(ack.ID) || ack.Status != "pending" || ack.TrainingEligible || ack.AutomaticResponseAuthorized {
		return errors.New("invalid learning acknowledgement")
	}
	return nil
}
