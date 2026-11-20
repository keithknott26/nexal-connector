package sandbox

import (
	"reflect"
	"testing"
)

func TestManagedSidecarArgsWithoutSlicePrefix(t *testing.T) {
	got := ManagedSidecarArgs(ManagedConfig{}, "t1")
	want := []string{"--label", "nexal.tenant=t1", "--label", "nexal.managed=1",
		"--memory=384m", "--memory-swap=384m", "--pids-limit=512"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestManagedSidecarArgsAddsCgroupParentWithSlicePrefix(t *testing.T) {
	c := ManagedConfig{SlicePrefix: "nexal"}
	got := ManagedSidecarArgs(c, "t1")
	want := "--cgroup-parent=" + ManagedSliceName(c.SlicePrefix, "t1")
	if got[len(got)-1] != want {
		t.Fatalf("last arg = %q, want %q (args %v)", got[len(got)-1], want, got)
	}
}

func TestLinkResultMinMbpsIsSlowerDirection(t *testing.T) {
	if got := (LinkResult{UpMbps: 10, DownMbps: 50}).MinMbps(); got != 10 {
		t.Fatalf("got %v, want 10", got)
	}
	if got := (LinkResult{UpMbps: 80, DownMbps: 20}).MinMbps(); got != 20 {
		t.Fatalf("got %v, want 20", got)
	}
}
