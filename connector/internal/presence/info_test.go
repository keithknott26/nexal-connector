package presence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/sysinfo"
)

func TestHostInfoSentAfterSnapshotAndPeersRecorded(t *testing.T) {
	conn := newFake(nil,
		`{"v":1,"type":"snapshot","online":["self","peer1"],"infos":{"peer1":{"os":"macOS 27.0","chip":"Apple M4","tunnelAddress":"100.86.1.2"}},"at":"x"}`,
		`{"v":1,"type":"host.info","hostId":"peer2","info":{"os":"macOS 26.1","batteryPercent":80,"batteryState":"charging"},"at":"x"}`,
		`{"v":1,"type":"host.info","hostId":"self","info":{"os":"spoofed"},"at":"x"}`)
	sent := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := New(Options{
		HostID: "self",
		Dial:   func(context.Context) (Conn, error) { return conn, nil },
		Info: func(context.Context) sysinfo.Info {
			select {
			case sent <- struct{}{}:
			default:
			}
			return sysinfo.Info{OS: "macOS 27.0", Cores: 10}
		},
		InfoEvery: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Run(ctx) }()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("host.info was never collected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		written := append([]string(nil), conn.written...)
		conn.mu.Unlock()
		infos := c.Snapshot().Infos
		var frame map[string]any
		for _, w := range written {
			if strings.Contains(w, `"host.info"`) {
				_ = json.Unmarshal([]byte(w), &frame)
			}
		}
		if frame != nil && len(infos) == 2 {
			if infos["peer1"].TunnelAddress != "100.86.1.2" || infos["peer2"].BatteryPercent != 80 {
				t.Fatalf("infos = %+v", infos)
			}
			if _, spoofed := infos["self"]; spoofed {
				t.Fatal("own id must not be recorded as a peer")
			}
			if frame["info"].(map[string]any)["os"] != "macOS 27.0" {
				t.Fatalf("frame = %v", frame)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("written=%v infos=%v", written, infos)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
