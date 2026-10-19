package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexal/connector/internal/remoteinference"
)

func TestRemoteInferenceRoutesAndValidation(t *testing.T) {
	var got []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v2/hosts/h1/remote-inference":
			if strings.Contains(string(b), "targetHostIds") {
				_, _ = w.Write([]byte(`{"tasks":[{"id":"t1"},{"id":"t2"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"t1","targetHostId":"h2","modelId":"m"}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"tasks":[{"id":"t1"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if tasks, err := c.RemoteInferenceTasks(ctx, "h1"); err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}
	if tasks, err := c.AllRemoteInference(ctx, "h1"); err != nil || len(tasks) != 1 {
		t.Fatalf("all: %+v %v", tasks, err)
	}
	task, err := c.CreateRemoteInference(ctx, "h1", "h2", "m")
	if err != nil || task.ID != "t1" || task.TargetHostID != "h2" {
		t.Fatalf("create: %+v %v", task, err)
	}
	var create map[string]string
	if err := json.Unmarshal([]byte(bodies[2]), &create); err != nil || create["targetHostId"] != "h2" || create["modelId"] != "m" {
		t.Fatalf("create body: %q", bodies[2])
	}
	if pool, err := c.CreateRemoteInferencePool(ctx, "h1", []string{"h2", "h3"}, "m"); err != nil || len(pool) != 2 {
		t.Fatalf("pool: %+v %v", pool, err)
	}
	if err := c.ReportRemoteInference(ctx, "h1", remoteinference.Report{TaskID: "t1", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelRemoteInference(ctx, "h1", "t1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v2/hosts/h1/remote-inference?role=target",
		"GET /api/v2/hosts/h1/remote-inference",
		"POST /api/v2/hosts/h1/remote-inference",
		"POST /api/v2/hosts/h1/remote-inference",
		"POST /api/v2/hosts/h1/remote-inference-state",
		"POST /api/v2/hosts/h1/remote-inference/t1/cancel",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	n := len(got)
	if _, err := c.RemoteInferenceTasks(ctx, "bad id/../x"); err == nil {
		t.Fatal("invalid host id accepted")
	}
	if err := c.CancelRemoteInference(ctx, "h1", "../x"); err == nil {
		t.Fatal("invalid task id accepted")
	}
	if len(got) != n {
		t.Fatalf("invalid ids reached the network: %v", got[n:])
	}
}

func TestCreateRemoteInferencePoolEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateRemoteInferencePool(context.Background(), "h1", []string{"h2"}, "m"); err == nil {
		t.Fatal("empty pool response accepted")
	}
}

func TestReportRemoteInferenceRetriesWithoutProgress(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "progress") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_schema"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	p := 0.5
	err = c.ReportRemoteInference(context.Background(), "h1", remoteinference.Report{TaskID: "t1", State: "downloading", Progress: &p})
	if err != nil {
		t.Fatalf("expected fallback to succeed: %v", err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "progress") || strings.Contains(bodies[1], "progress") {
		t.Fatalf("bodies: %q", bodies)
	}
}
