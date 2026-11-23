package pool

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The inbox must refuse malformed or out-of-contract requests with a specific
// status, before any frame reaches the mailbox, and must answer a replayed
// frame with a conflict rather than accepting it twice.
func TestRingHandlerRejectsMalformedRequestsAndDuplicates(t *testing.T) {
	set := newRingSet(t, 2, testTenant, 3*time.Second, []bool{true, true})
	sender := set.rings[0]
	client := sender.transport.clients[set.fprints[1]]
	frame := collectiveFrame{kind: collectiveKindChunk, phase: collectivePhaseReduceScatter,
		op: ReduceSum, senderRank: sender.Rank(), step: 0, chunk: 0,
		session: testSession, tenant: testTenant, total: 2, values: []float64{1}}
	valid, err := encodeCollectiveFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	abort := frame
	abort.kind, abort.phase, abort.op, abort.values, abort.total = collectiveKindAbort, 0, 0, nil, 0
	abort.failedRank, abort.reason = 5, "boom" // a two-rank ring has no rank 5
	badAbort, err := encodeCollectiveFrame(abort)
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path string, body []byte, header map[string]string) int {
		t.Helper()
		req, err := http.NewRequest(method, client.endpoint+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := client.client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
		header map[string]string
		want   int
	}{
		{"wrong method", http.MethodGet, collectivePath, nil, nil, http.StatusMethodNotAllowed},
		{"wrong path", http.MethodPost, "/v1/other", valid, nil, http.StatusNotFound},
		{"query string", http.MethodPost, collectivePath + "?x=1", valid, nil, http.StatusNotFound},
		{"shorter than a header", http.MethodPost, collectivePath, valid[:collectiveHeaderBytes-1], nil, http.StatusRequestEntityTooLarge},
		{"content encoding", http.MethodPost, collectivePath, valid, map[string]string{"Content-Encoding": "gzip"}, http.StatusRequestEntityTooLarge},
		{"undecodable frame", http.MethodPost, collectivePath, []byte(strings.Repeat("\xff", collectiveHeaderBytes)), nil, http.StatusBadRequest},
		{"abort naming a rank outside the ring", http.MethodPost, collectivePath, badAbort, nil, http.StatusBadRequest},
		{"well-formed frame", http.MethodPost, collectivePath, valid, nil, http.StatusNoContent},
		{"replayed frame", http.MethodPost, collectivePath, valid, nil, http.StatusConflict},
	}
	for _, tc := range cases {
		if got := do(tc.method, tc.path, tc.body, tc.header); got != tc.want {
			t.Errorf("%s: HTTP %d, want %d", tc.name, got, tc.want)
		}
	}
}
