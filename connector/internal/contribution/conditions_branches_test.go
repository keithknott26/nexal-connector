package contribution

import (
	"strings"
	"testing"
)

func TestThermalConditionBranches(t *testing.T) {
	cases := []struct {
		name         string
		s            Signals
		stale        bool
		known, wh    bool
		valueContain string
	}{
		{"unknown", Signals{Thermal: ThermalUnknown}, false, false, false, "unknown"},
		{"stale hides a throttled reading", Signals{Thermal: ThermalThrottled, ThermalSpeedLimit: 50}, true, false, false, "unknown"},
		{"nominal from a bridge", Signals{Thermal: ThermalNominal}, false, true, false, "nominal"},
		{"throttled without a limit invents no number", Signals{Thermal: ThermalThrottled}, false, true, true, "thermally throttled"},
		{"throttled with a limit", Signals{Thermal: ThermalThrottled, ThermalSpeedLimit: 62}, false, true, true, "62%"},
	}
	for _, tc := range cases {
		c := thermalCondition(tc.s, tc.stale)
		if c.Known != tc.known || c.Withholding != tc.wh || !strings.Contains(c.Value, tc.valueContain) {
			t.Errorf("%s: got %+v", tc.name, c)
		}
		if tc.name == "throttled without a limit invents no number" && strings.Contains(c.Value, "%") {
			t.Errorf("%s: value must not carry a percentage: %q", tc.name, c.Value)
		}
	}
}

func TestOwnerConditionBranches(t *testing.T) {
	cases := []struct {
		name      string
		s         Signals
		known, wh bool
		contain   string
	}{
		{"unknown never withholds", Signals{OwnerActive: true}, false, false, "unknown"},
		{"active withholds", Signals{OwnerActiveKnown: true, OwnerActive: true, IdleSeconds: 3}, true, true, "owner active (idle 3s)"},
		{"override does not withhold", Signals{OwnerActiveKnown: true, OwnerActive: true, OwnerOverride: true, IdleSeconds: 3}, true, false, "overridden"},
		{"idle", Signals{OwnerActiveKnown: true, IdleSeconds: 900}, true, false, "idle 900s"},
	}
	for _, tc := range cases {
		c := ownerCondition(tc.s)
		if c.Known != tc.known || c.Withholding != tc.wh || !strings.Contains(c.Value, tc.contain) {
			t.Errorf("%s: got %+v", tc.name, c)
		}
	}
}
