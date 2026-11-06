package sandbox

import "testing"

func TestHostingConfigNormalize(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        HostingConfig
		wantMax   int
		wantPlace string
		wantErr   bool
	}{
		{"defaults", HostingConfig{}, 5, PlacementMembers, false},
		{"any alias", HostingConfig{Placement: "any"}, 5, PlacementMembers, false},
		{"mine alias", HostingConfig{Placement: "mine"}, 5, PlacementOwner, false},
		{"owner kept", HostingConfig{MaxSandboxes: 7, Placement: PlacementOwner}, 7, PlacementOwner, false},
		{"unmanaged upper bound", HostingConfig{MaxSandboxes: MaxUnmanagedSandboxes}, MaxUnmanagedSandboxes, PlacementMembers, false},
		{"unmanaged over limit", HostingConfig{MaxSandboxes: MaxUnmanagedSandboxes + 1}, 0, "", true},
		{"negative", HostingConfig{MaxSandboxes: -1}, 0, "", true},
		{"managed over limit", HostingConfig{Managed: true, MaxSandboxes: MaxManagedSandboxes + 1}, 0, "", true},
		{"unknown placement", HostingConfig{Placement: "everyone"}, 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Normalize()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if got != (HostingConfig{}) {
					t.Fatalf("error must return the zero config, got %+v", got)
				}
				return
			}
			if got.MaxSandboxes != tc.wantMax || got.Placement != tc.wantPlace {
				t.Fatalf("got max=%d placement=%q, want %d %q", got.MaxSandboxes, got.Placement, tc.wantMax, tc.wantPlace)
			}
		})
	}
}
