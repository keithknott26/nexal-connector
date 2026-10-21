package main

import (
	"bytes"
	"testing"
)

func TestGibText(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0.00 GiB"},
		{1 << 30, "1.00 GiB"},
		{3 << 29, "1.50 GiB"},
		{5 << 30, "5.00 GiB"},
	} {
		if got := gibText(tc.in); got != tc.want {
			t.Errorf("gibText(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteLineAppendsNewline(t *testing.T) {
	var b bytes.Buffer
	writeLine(&b, "model %s: %d files", "m1", 3)
	if got, want := b.String(), "model m1: 3 files\n"; got != want {
		t.Fatalf("writeLine = %q, want %q", got, want)
	}
}

func TestHostLocal(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"mac":           "mac.local",
		"mac.local":     "mac.local",
		"mac.example.c": "mac.example.c",
	} {
		if got := hostLocal(in); got != want {
			t.Errorf("hostLocal(%q) = %q, want %q", in, got, want)
		}
	}
}
