package e2e

// The two policy switches added on 2026-09-06, end to end: from the PATCH a parent's console sends
// to the `user_restrictions` list the phone is handed.
//
// Both are about a restriction NOT being applied, which is the hardest kind of assertion to write
// honestly. An absence passes for the wrong reason whenever the thing was never going to be there —
// a typo in the restriction name, a fixture whose policy was never fetched, a desired state that
// came back empty. So each half here is paired with the state that must still contain it: the
// switch off, in the same test, against the same device. A restriction that is missing from both is
// a broken assertion, not a working feature.

import (
	"net/http"
	"testing"
)

const (
	restrictionDebugging  = "no_debugging_features"
	restrictionPrivateDNS = "disallow_config_private_dns"
)

// FR-5.6.
func TestDeveloperOptionsCanBeAllowedPerChild(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// The default, asserted rather than assumed: this is the calibration for everything below it.
	// If a fresh child did not have the restriction, the "it is gone" half would prove nothing.
	pol := h.policy(f.parent.Token, f.child.ID)
	if pol.AllowDebugging {
		t.Fatal("a new child defaults to allowing developer options; the restriction that switches " +
			"adb off must be the state a parent has to leave, not one they have to reach")
	}
	before := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	mustHave(t, before.UserRestrictions, restrictionDebugging,
		"a child with the switch off must have developer options restricted")

	// On.
	patched := h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": true})
	if !patched.AllowDebugging {
		t.Fatalf("the PATCH did not stick: allow_debugging is %v", patched.AllowDebugging)
	}
	if patched.Version <= pol.Version {
		t.Fatalf("the policy version did not move (%d → %d), so no phone would notice the change",
			pol.Version, patched.Version)
	}

	allowed := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	mustNotHave(t, allowed.UserRestrictions, restrictionDebugging,
		"allowing developer options must withhold the restriction that switches adb off")

	// And nothing else moved with it. Named individually rather than compared as a set, so the
	// failure says which restriction went missing rather than printing two lists.
	for _, still := range []string{
		"no_safe_boot", "no_config_date_time", "no_add_user",
		"no_install_unknown_sources", "no_uninstall_apps",
	} {
		mustHave(t, allowed.UserRestrictions, still,
			"the developer-options switch must not loosen "+still)
	}

	// Off again. No policy state may be one-way (FR-4.2's rule, applied to this switch).
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": false})
	mustHave(t, h.desiredState(f.parent.Token, f.device.ID, "").Desired.UserRestrictions,
		restrictionDebugging, "turning the switch back off must restore the restriction")
}

// FR-6.1: no resolver by default, and the lock that protects one is coupled to there being one.
func TestNoFilteringResolverIsConfiguredByDefault(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	pol := h.policy(f.parent.Token, f.child.ID)
	if pol.DNSHost != "" {
		t.Fatalf("a new child is pointed at the resolver %q. No filtering endpoint is chosen for a "+
			"family by default — one that is picked for them is a choice nobody made, and a DoT "+
			"resolver cannot remove advertising served inside an app anyway.", pol.DNSHost)
	}

	empty := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if empty.PrivateDNSHost != "" {
		t.Fatalf("the device is told to use private DNS host %q", empty.PrivateDNSHost)
	}
	mustNotHave(t, empty.UserRestrictions, restrictionPrivateDNS,
		"with no resolver configured there is no DNS policy to protect, so locking the setting "+
			"takes it away from the parent too and defends nothing")

	// Setting one is still possible, and it brings the lock with it. This is the positive control:
	// without it, the assertion above passes just as well on a build where the restriction name was
	// misspelled and can never appear at all.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"dns_host": "kids.example-dns.test"})
	set := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if set.PrivateDNSHost != "kids.example-dns.test" {
		t.Fatalf("the configured resolver did not reach the device: %q", set.PrivateDNSHost)
	}
	mustHave(t, set.UserRestrictions, restrictionPrivateDNS,
		"a configured resolver must be locked, or a child can undo it in Settings")

	// And clearing it is allowed — it used to be the one field the API refused to empty, on the
	// reasoning that there was a sensible default to fall back to. There is not one any more.
	cleared := h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"dns_host": ""})
	if cleared.DNSHost != "" {
		t.Fatalf("clearing the resolver left %q behind", cleared.DNSHost)
	}
	after := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if after.PrivateDNSHost != "" {
		t.Fatalf("after clearing, the device is still told to use %q", after.PrivateDNSHost)
	}
	mustNotHave(t, after.UserRestrictions, restrictionPrivateDNS,
		"clearing the resolver must release the lock with it")
}

// policy reads a child's policy the way the console does.
func (h *harness) policy(token, childID string) policyDTO {
	h.t.Helper()
	var pol policyDTO
	h.call(http.MethodGet, "/children/"+childID+"/policy", token, nil).
		expect(http.StatusOK).decode(&pol)
	return pol
}
