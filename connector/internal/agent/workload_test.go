package agent

import (
	"context"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

func TestValidateAttemptBounds(t *testing.T) {
	now := time.Now()
	good := client.Attempt{ID: "a1", JobID: "j1", HostID: "h1", Template: Template, Samples: 10, LeaseExpiresAt: now.Add(time.Minute)}
	if err := ValidateAttempt(good, "h1", now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*client.Attempt){
		"bad attempt id":   func(a *client.Attempt) { a.ID = "../x" },
		"bad job id":       func(a *client.Attempt) { a.JobID = "" },
		"wrong host":       func(a *client.Attempt) { a.HostID = "other" },
		"unknown template": func(a *client.Attempt) { a.Template = "shell" },
		"zero samples":     func(a *client.Attempt) { a.Samples = 0 },
		"too many samples": func(a *client.Attempt) { a.Samples = MaxSamples + 1 },
		"negative cost":    func(a *client.Attempt) { a.MaxCostCents = -1 },
		"expired lease":    func(a *client.Attempt) { a.LeaseExpiresAt = now },
		"overlong lease":   func(a *client.Attempt) { a.LeaseExpiresAt = now.Add(3 * time.Minute) },
	} {
		a := good
		mutate(&a)
		if ValidateAttempt(a, "h1", now) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMonteCarloRejectsUnsupportedAndExpired(t *testing.T) {
	a := client.Attempt{ID: "a1", Template: Template, Samples: 10}
	future := func() time.Time { return time.Now().Add(time.Minute) }
	past := func() time.Time { return time.Now().Add(-time.Second) }
	bad := a
	bad.Template = "other"
	if _, err := MonteCarlo(context.Background(), bad, future); err == nil {
		t.Error("unknown template ran")
	}
	bad = a
	bad.Samples = MaxSamples + 1
	if _, err := MonteCarlo(context.Background(), bad, future); err == nil {
		t.Error("oversized sample count ran")
	}
	if _, err := MonteCarlo(context.Background(), a, past); err == nil {
		t.Error("expired lease ignored")
	}
}
