package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// pool-serve must reject bad input with a coded invalid_arguments error before it reaps processes,
// checks the model or starts anything. Every case here fails during argument or address validation.
func TestInferencePoolServeRejectsBadArguments(t *testing.T) {
	cfg := "/tmp/nexal-pool-args-test/config.json"
	hosts := "100.64.0.1:8010,100.64.0.2:8010"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no model id", nil, "usage: nexal inference pool-serve"},
		{"unknown flag", []string{"m1", "--nope"}, "invalid command flags"},
		{"extra positional", []string{"m1", "--config", cfg, "extra"}, "unexpected positional arguments"},
		{"relative config", []string{"m1", "--config", "config.json"}, "--config must be an absolute path"},
		{"bad model id", []string{"bad id!", "--config", cfg, "--solo", "--api-host", "100.64.0.1"}, "invalid model id"},
		{"solo without api host", []string{"m1", "--config", cfg, "--solo"}, "--api-host must be one specific IP address"},
		{"solo with wildcard api host", []string{"m1", "--config", cfg, "--solo", "--api-host", "0.0.0.0"}, "--api-host must be one specific IP address"},
		{"pool without hosts", []string{"m1", "--config", cfg, "--rank", "0", "--api-host", "100.64.0.1"}, "a pool needs at least two members"},
		{"single member pool", []string{"m1", "--config", cfg, "--rank", "0", "--hosts", "100.64.0.1:8010", "--api-host", "100.64.0.1"}, "a pool needs at least two members"},
		{"rank unset", []string{"m1", "--config", cfg, "--hosts", hosts, "--api-host", "100.64.0.1"}, "rank -1 is outside the pool"},
		{"rank too high", []string{"m1", "--config", cfg, "--rank", "2", "--hosts", hosts, "--api-host", "100.64.0.1"}, "rank 2 is outside the pool"},
		{"member without port", []string{"m1", "--config", cfg, "--rank", "0", "--hosts", "100.64.0.1,100.64.0.2:8010", "--api-host", "100.64.0.1"}, "invalid pool member"},
		{"member with low port", []string{"m1", "--config", cfg, "--rank", "0", "--hosts", "100.64.0.1:80,100.64.0.2:8010", "--api-host", "100.64.0.1"}, "invalid pool member"},
		{"member not ipv4", []string{"m1", "--config", cfg, "--rank", "0", "--hosts", "[::1]:8010,100.64.0.2:8010", "--api-host", "100.64.0.1"}, "invalid pool member"},
		{"pool wildcard api host", []string{"m1", "--config", cfg, "--rank", "0", "--hosts", hosts, "--api-host", "0.0.0.0"}, "--api-host must be one specific IP address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := inferencePoolServe(context.Background(), c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error containing %q", err, c.want)
			}
			var ce *codedError
			if !errors.As(err, &ce) || ce.code != "invalid_arguments" {
				t.Fatalf("got %#v, want a coded invalid_arguments error", err)
			}
		})
	}
}

func TestInferencePoolSetupRejectsBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":    {"--nope"},
		"positional":      {"extra"},
		"relative config": {"--config", "config.json"},
	} {
		t.Run(name, func(t *testing.T) {
			err := inferencePoolSetup(context.Background(), args)
			var ce *codedError
			if !errors.As(err, &ce) || ce.code != "invalid_arguments" {
				t.Fatalf("got %#v, want a coded invalid_arguments error", err)
			}
		})
	}
}
