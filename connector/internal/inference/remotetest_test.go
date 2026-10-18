package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRemoteEndpoint(t *testing.T) {
	for _, ok := range []string{"http://100.86.1.2:8080/v1", "http://127.0.0.1:8080", "http://100.64.0.1:8080/v1/"} {
		if _, err := ValidateRemoteEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://100.86.1.2/v1", "http://example.com/v1", "http://8.8.8.8/v1", "http://u:p@100.86.1.2/v1", "http://100.86.1.2/v1?x=1"} {
		if _, err := ValidateRemoteEndpoint(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRemoteTestMeasuresAndChecksTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen3-4b"}]}`)
		case "/v1/chat/completions":
			if !strings.Contains(r.Header.Get("content-type"), "json") {
				w.WriteHeader(400)
				return
			}
			for _, c := range []string{"4", "2"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	r, err := RemoteTest(context.Background(), srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "qwen3-4b" || r.Tokens != 2 || !r.CapabilityOK || r.Reply != "42" || r.FirstTokenMs <= 0 || r.TotalMs < r.FirstTokenMs {
		t.Fatalf("%+v", r)
	}
}
