package main

import (
	"context"
	"strings"
	"testing"
)

// The top-level dispatcher must reject a missing, unknown, or malformed
// command before any subcommand touches config or the network.
func TestRunDispatchRejectsBadTopLevelInvocations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments prints usage", nil, "usage: nexal"},
		{"unknown command", []string{"definitely-not-a-command"}, "unknown command"},
		{"share-while-active without mode", []string{"share-while-active"}, "usage: nexal share-while-active on|off"},
		{"share-while-active bad mode", []string{"share-while-active", "maybe"}, "usage: nexal share-while-active on|off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%v) error = %v, want substring %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestRunVersionSucceeds(t *testing.T) {
	if err := run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
}
