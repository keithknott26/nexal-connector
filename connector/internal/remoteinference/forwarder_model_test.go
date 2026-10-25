package remoteinference

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A pool's server lists nothing for a model loaded from a folder; the forwarder lists the model itself and
// passes requests that name it on as "default_model".
func TestForwarderNamesThePoolModel(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	addr := freeAddr(t)
	f, err := NewForwarder(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SetUpstream(strings.TrimPrefix(up.URL, "http://"), "")
	f.SetModel("qwen3-4b-q4")
	if code, body := get(t, "http://"+addr+"/v1/models"); code != 200 || !strings.Contains(body, `"id":"qwen3-4b-q4"`) {
		t.Fatalf("models: %d %s", code, body)
	}
	post := func(body string) {
		resp, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post(`{"model":"qwen3-4b-q4","messages":[]}`)
	if !strings.Contains(got, `"model":"default_model"`) || !strings.Contains(got, `"messages":[]`) {
		t.Fatalf("not renamed: %s", got)
	}
	post(`{"model":"other","messages":[]}`)
	if got != `{"model":"other","messages":[]}` {
		t.Fatalf("other name changed: %s", got)
	}
}
