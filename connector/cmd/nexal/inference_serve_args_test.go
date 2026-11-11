package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// serve and remote-test must reject malformed arguments with a coded invalid_arguments error
// before they look at the model directory, bind a port or contact an endpoint.
func TestInferenceServeAndRemoteTestRejectBadArguments(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context, []string) error
		args []string
		want string
	}{
		{"serve no model id", inferenceServe, nil, "usage: nexal inference serve"},
		{"serve unknown flag", inferenceServe, []string{"m1", "--nope"}, "invalid command flags"},
		{"serve extra positional", inferenceServe, []string{"m1", "--config", "/tmp/nexal-serve-args/config.json", "extra"}, "unexpected positional arguments"},
		{"serve relative config", inferenceServe, []string{"m1", "--config", "config.json"}, "--config must be an absolute path"},
		{"serve non-numeric port", inferenceServe, []string{"m1", "--port", "abc"}, "invalid command flags"},
		{"remote-test no endpoint", inferenceRemoteTest, nil, "usage: nexal inference remote-test"},
		{"remote-test unknown flag", inferenceRemoteTest, []string{"100.64.0.1:8010", "--nope"}, "invalid command flags"},
		{"remote-test relative config", inferenceRemoteTest, []string{"100.64.0.1:8010", "--config", "config.json"}, "--config must be an absolute path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run(context.Background(), c.args)
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
