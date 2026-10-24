package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDevPodAlive(t *testing.T) {
	ctx := context.Background()
	if _, err := (&DevPod{}).Alive(ctx, "bad id!"); err == nil {
		t.Fatal("invalid workspace id must be rejected")
	}
	cases := []struct {
		name    string
		cid     string // answer to the container lookup
		running string // answer to the running-sidecar lookup
		psErr   error
		want    bool
		wantErr bool
	}{
		{name: "running", cid: "c1\n", running: "s1\n", want: true},
		{name: "no container", cid: "", running: "s1\n", want: false},
		{name: "sidecar stopped", cid: "c1\n", running: " \n", want: false},
		{name: "docker error", psErr: errors.New("exit 1"), wantErr: true},
	}
	for _, c := range cases {
		d, _ := scriptedDevPod(t, func(line string) ([]byte, error) {
			if c.psErr != nil {
				return nil, c.psErr
			}
			if strings.Contains(line, "name="+SidecarName("ws1")) {
				return []byte(c.running), nil
			}
			return []byte(c.cid), nil
		})
		got, err := d.Alive(ctx, "ws1")
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: Alive = %v, %v; want %v (err %v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}
