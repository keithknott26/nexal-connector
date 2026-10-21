package config

import (
	"strings"
	"testing"
)

func TestCheckJSONObject(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70)
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"empty object", `{}`, ""},
		{"nested ok", `{"a":{"b":[1,{"c":2}]},"d":"x"}`, ""},
		{"same key in sibling objects ok", `{"a":{"k":1},"b":{"k":2}}`, ""},
		{"duplicate key", `{"a":1,"a":2}`, "ambiguous JSON member"},
		{"case alias", `{"Token":1,"token":2}`, "ambiguous JSON member"},
		{"kelvin alias", "{\"k\":1,\"K\":2}", "ambiguous JSON member"},
		{"nested duplicate", `{"a":[{"x":1,"X":2}]}`, "ambiguous JSON member"},
		{"not object", `[1]`, "JSON object required"},
		{"scalar", `1`, "JSON object required"},
		{"trailing data", `{} {}`, "trailing JSON data"},
		{"invalid", `{"a":`, "invalid JSON"},
		{"too deep", deep, "JSON nesting limit exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckJSONObject([]byte(tc.in))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckJSONObjectErrorOmitsInput(t *testing.T) {
	err := CheckJSONObject([]byte(`{"secret-token":"hunter2","secret-token":"x"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaks input: %v", err)
	}
}
