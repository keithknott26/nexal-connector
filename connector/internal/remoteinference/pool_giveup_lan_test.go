package remoteinference

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestGiveUpLANFallsBackOnlyForAnEarlyLANFailure(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		pooledLAN bool
		since     time.Time
		wantOff   bool
	}{
		{"never formed over the LAN", true, time.Time{}, true},
		{"failed before stableAfter", true, now.Add(-time.Minute), true},
		{"ran long enough to count as stable", true, now.Add(-2 * stableAfter), false},
		{"not a LAN pool", false, time.Time{}, false},
	}
	for _, c := range cases {
		l := &leader{
			r:         &Runner{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
			pooledLAN: c.pooledLAN,
		}
		l.giveUpLAN(c.since)
		if l.lanOff != c.wantOff {
			t.Errorf("%s: lanOff = %v, want %v", c.name, l.lanOff, c.wantOff)
		}
		if l.pooledLAN {
			t.Errorf("%s: pooledLAN must always be cleared", c.name)
		}
	}
}
