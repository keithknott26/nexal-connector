package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeServer(t *testing.T, failCall int) string {
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"m"}]}`) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == failCall {
			http.Error(w, "boom", 500)
			return
		}
		for i := 0; i < 5; i++ {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi \"}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return strings.Replace(s.URL, "127.0.0.1", "127.0.0.1", 1) + "/v1"
}

func TestRemoteSweepRecordsEveryCallAndSurvivesOneFailure(t *testing.T) {
	ep := fakeServer(t, 2)
	seen := 0
	res, err := RemoteSweep(context.Background(), ep, 4, nil, func(SweepSample) { seen++ })
	if err != nil || len(res.Samples) != 4 || seen != 4 {
		t.Fatal(err, res, seen)
	}
	if res.Samples[1].OK || res.Samples[1].Error == "" || !res.Samples[0].OK || res.Samples[0].Tokens != 5 {
		t.Fatalf("%+v", res.Samples)
	}
}

func TestRemoteSweepRejectsBadCallCountsAndFailsWhenAllFail(t *testing.T) {
	if _, err := RemoteSweep(context.Background(), "http://127.0.0.1:1/v1", 1, nil, nil); err == nil {
		t.Fatal("1 call is not a sweep")
	}
	if _, err := RemoteSweep(context.Background(), "http://127.0.0.1:1/v1", 2, nil, nil); err == nil {
		t.Fatal("unreachable endpoint must fail")
	}
}
