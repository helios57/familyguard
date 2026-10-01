package main

import (
	"strings"
	"testing"

	"github.com/helios57/familyguard/backend/internal/store"
)

// get_device's background_restriction is the only place an MCP client learns why Ring and Lock
// arrive late on this phone; the advice must name exactly the settings that are off, and be absent
// when nothing is — or when the phone has not said (nil is "not reported", never "restricted").
func TestRestrictionAdviceNamesWhatIsOff(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name         string
		state        store.DeviceState
		want, absent []string
	}{
		{"nothing reported", store.DeviceState{}, nil, nil},
		{"both granted", store.DeviceState{PowerExempt: &yes, ExactAlarms: &yes}, nil, nil},
		{"battery restricted", store.DeviceState{PowerExempt: &no, ExactAlarms: &yes}, []string{"Unrestricted"}, []string{"Alarms and reminders"}},
		{"alarms denied", store.DeviceState{PowerExempt: &yes, ExactAlarms: &no}, []string{"Alarms and reminders"}, []string{"Unrestricted"}},
	} {
		got := restrictionAdvice(&tc.state)
		if tc.want == nil {
			if got != nil {
				t.Errorf("%s: advice %v, want none", tc.name, got)
			}
			continue
		}
		steps := strings.Join(got["remedy"].([]string), "\n")
		for _, w := range tc.want {
			if !strings.Contains(steps, w) {
				t.Errorf("%s: the remedy does not say %q: %s", tc.name, w, steps)
			}
		}
		for _, a := range tc.absent {
			if strings.Contains(steps, a) {
				t.Errorf("%s: the remedy names %q, which is not off: %s", tc.name, a, steps)
			}
		}
	}
}
