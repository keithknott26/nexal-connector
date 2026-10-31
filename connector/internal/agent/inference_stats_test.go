package agent

import (
	"context"
	"testing"

	"nexal/connector/internal/client"
)

type statsAPI struct {
	fakeAPI
	seen   []*client.InferenceStats
	reject bool // behave like a coordinator that predates the field
}

func (s *statsAPI) Heartbeat(_ context.Context, _ string, h client.Heartbeat) (client.HeartbeatResponse, error) {
	s.seen = append(s.seen, h.Inference)
	if s.reject && h.Inference != nil {
		return client.HeartbeatResponse{}, &client.StatusError{Status: 400, Code: "invalid_schema"}
	}
	return client.HeartbeatResponse{}, nil
}

func TestHeartbeatCarriesTheInferenceSummaryWhenThereIsOne(t *testing.T) {
	a, _ := testAgent(t)
	api := &statsAPI{}
	a.api = api
	if err := a.hostHeartbeat(context.Background()); err != nil || api.seen[0] != nil {
		t.Fatalf("no supplier: err=%v seen=%v", err, api.seen)
	}
	a.SetInferenceStats(func() *client.InferenceStats { return &client.InferenceStats{LocalRequests: 3} })
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := api.seen[1]; got == nil || got.LocalRequests != 3 {
		t.Fatalf("summary not attached: %+v", got)
	}
	a.SetInferenceStats(func() *client.InferenceStats { return nil })
	_ = a.hostHeartbeat(context.Background())
	if api.seen[2] != nil {
		t.Fatal("a nil summary must send nothing")
	}
}

func TestAnOlderCoordinatorThatRefusesTheFieldDoesNotTakeTheMacOffline(t *testing.T) {
	a, _ := testAgent(t)
	api := &statsAPI{reject: true}
	a.api = api
	a.SetInferenceStats(func() *client.InferenceStats { return &client.InferenceStats{LocalRequests: 1} })
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatalf("the heartbeat must succeed without the summary: %v", err)
	}
	if len(api.seen) != 2 || api.seen[0] == nil || api.seen[1] != nil {
		t.Fatalf("want one try with the summary then one without, got %v", api.seen)
	}
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.seen) != 3 || api.seen[2] != nil {
		t.Fatalf("the summary must stay off for a while: %v", api.seen)
	}
}
