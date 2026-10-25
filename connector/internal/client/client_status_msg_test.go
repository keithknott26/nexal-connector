package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusErrorCarriesCoordinatorMessage(t *testing.T) {
	status := 409
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"code":"already_running","message":"A pool member is already\nserving this model."}}`))
	}))
	defer server.Close()
	api, _ := New(server.URL, "host-secret", true)
	_, err := api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", "", true)
	if err == nil || err.Error() != "coordinator rejected request (HTTP 409): A pool member is already serving this model." {
		t.Fatalf("got %v", err)
	}
	status = 502
	_, err = api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", "", true)
	if err == nil || strings.Contains(err.Error(), "pool member") {
		t.Fatalf("5xx detail leaked: %v", err)
	}
}
