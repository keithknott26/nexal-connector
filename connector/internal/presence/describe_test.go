package presence

import (
	"errors"
	"fmt"
	"testing"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wsclient"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestDescribeMapsErrorsToFixedReasons(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "the coordinator closed the presence stream"},
		{"401", &client.StatusError{Status: 401}, "the coordinator did not accept this host's credential"},
		{"403 wrapped", fmt.Errorf("dial: %w", &client.StatusError{Status: 403}), "the coordinator did not accept this host's credential"},
		{"500", &client.StatusError{Status: 500}, "the coordinator refused the presence stream"},
		{"superseded", &wsclient.CloseError{Code: CloseSuperseded}, "another connection from this Mac replaced the presence stream"},
		{"other close", &wsclient.CloseError{Code: 1001}, "the coordinator closed the presence stream"},
		{"protocol", wsclient.ErrProtocol, "the presence stream sent invalid data"},
		{"too big", wsclient.ErrMessageTooBig, "the presence stream sent invalid data"},
		{"timeout", timeoutErr{}, "the presence stream went silent"},
		{"unknown", errors.New("secret detail"), "cannot reach the coordinator"},
	}
	for _, tc := range cases {
		if got := describe(tc.err); got != tc.want {
			t.Errorf("%s: describe = %q, want %q", tc.name, got, tc.want)
		}
	}
}
