package sandbox

import (
	"reflect"
	"testing"
)

func TestRuntimeCandidatesMacOSOrder(t *testing.T) {
	env := DevEnv{
		Home: "/h",
		Glob: func(string) []string { return []string{"/h/.colima/other/docker.sock"} },
	}
	got := RuntimeCandidates(env)
	want := []runtimeCandidate{
		{RuntimeOrbStack, "/h/.orbstack/run/docker.sock"},
		{RuntimeColima, "/h/.colima/default/docker.sock"},
		{RuntimeColima, "/h/.colima/docker.sock"},
		{RuntimeColima, "/h/.colima/other/docker.sock"},
		{RuntimeLima, "/h/.lima/docker/sock/docker.sock"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestRuntimeCandidatesLinuxPrefersNativeDocker(t *testing.T) {
	noGlob := func(string) []string { return nil }

	// A configured (e.g. rootless) socket is the only one tried.
	got := RuntimeCandidates(DevEnv{Home: "/h", GOOS: "linux", Glob: noGlob, DockerSocket: "/run/user/1000/docker.sock"})
	want := []runtimeCandidate{{RuntimeDocker, "/run/user/1000/docker.sock"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit socket: got %v, want %v", got, want)
	}

	// Otherwise the standard native sockets come first, ahead of VM runtimes.
	got = RuntimeCandidates(DevEnv{Home: "/h", GOOS: "linux", Glob: noGlob})
	if len(got) < 3 || got[0] != (runtimeCandidate{RuntimeDocker, "/var/run/docker.sock"}) ||
		got[1] != (runtimeCandidate{RuntimeDocker, "/run/docker.sock"}) || got[2].name != RuntimeOrbStack {
		t.Fatalf("linux default order wrong: %v", got)
	}
}
