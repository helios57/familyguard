package e2e

import (
	"net/http"
	"testing"
)

// A new setup code REVOKES the phone that is enrolled now, and nothing about asking for one says
// so. This is the regression test for the first real phone: on 2026-09-06 a parent tapped what the
// console called "Setup QR" on a working device — reading it as "show me that code again" — and
// disconnected it. ENROLLMENT_ISSUED at 02:01:18, an hour after DEVICE_ENROLLED at 00:55:27, and
// the phone could not come back: the DPC never re-enrolls while it still holds a credential, so
// the only way back is a factory reset.
//
// The property under test is not "the endpoint has a flag". It is that the credential of a working
// phone survives a request that did not say it meant to replace it (FR-1.7).
func TestANewSetupCodeDoesNotSilentlyRevokeAWorkingPhone(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// The positive control, taken BEFORE the refusal: this call is what "the phone still works"
	// means, so a test where it never worked would read as a passing guard.
	h.call(http.MethodGet, "/device/policy", f.enroll.DeviceToken, nil).expect(http.StatusOK)

	h.call(http.MethodPost, "/devices/"+f.device.ID+"/provisioning", f.parent.Token, nil).
		expectError(http.StatusConflict, "already_enrolled")

	// The refusal has to leave nothing behind. A guard that answers 409 after minting the token
	// would be the same wipe with an error message on top.
	h.call(http.MethodGet, "/device/policy", f.enroll.DeviceToken, nil).expect(http.StatusOK)

	var view deviceViewDTO
	h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).
		expect(http.StatusOK).decode(&view)
	if !view.Enrolled || view.Device.EnrolledAt == nil {
		t.Fatalf("the refused request un-enrolled the device anyway: enrolled=%v enrolled_at=%v",
			view.Enrolled, view.Device.EnrolledAt)
	}

	for _, e := range h.readAudit(f.parent.Token) {
		if e.Action == "ENROLLMENT_ISSUED" && e.TargetID == f.device.ID && e.Detail["replaced_enrolled"] == true {
			t.Fatal("the refused request was audited as a replacement, so something was issued")
		}
	}
}

// The other half: a parent who says they are replacing the phone gets the QR, and the old phone is
// revoked — which is the whole point of the action and must keep working. Without this the guard
// could be a permanent refusal and the test above would still pass.
func TestReplacingAPhoneOnPurposeRevokesTheOldOne(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	var out provisioningDTO
	h.call(http.MethodPost, "/devices/"+f.device.ID+"/provisioning", f.parent.Token,
		map[string]any{"replace_enrolled": true}).expect(http.StatusOK).decode(&out)

	// 401 and not 403: the credential is gone, not insufficient.
	h.call(http.MethodGet, "/device/policy", f.enroll.DeviceToken, nil).expect(http.StatusUnauthorized)

	var view deviceViewDTO
	h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).
		expect(http.StatusOK).decode(&view)
	if view.Enrolled || view.Device.EnrolledAt != nil {
		t.Fatalf("the device still reads as enrolled after being replaced: enrolled=%v enrolled_at=%v",
			view.Enrolled, view.Device.EnrolledAt)
	}

	// The audit is where a second parent finds out which of the two things happened, so the flag
	// being recorded is part of the behaviour and not a debugging aid.
	replaced := false
	for _, e := range h.readAudit(f.parent.Token) {
		if e.Action == "ENROLLMENT_ISSUED" && e.TargetID == f.device.ID && e.Detail["replaced_enrolled"] == true {
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("no ENROLLMENT_ISSUED row records that an enrolled device was replaced")
	}

	// And the new code enrolls a phone, so the parent is not left holding a QR that does nothing.
	admin, ok := out.Payload[extraAdminExtras].(map[string]any)
	if !ok {
		t.Fatalf("provisioning payload has no admin extras bundle: %+v", out.Payload)
	}
	token, _ := admin["enrollment_token"].(string)
	if token == "" {
		t.Fatalf("admin extras carry no enrollment token: %+v", admin)
	}
	// The type-able copy the console shows next to the QR (FR-1.8) has to be the SAME token. A
	// second one would enroll a phone and leave the QR dead, or the other way round, and which of
	// the two a parent used would decide whether their phone came back.
	if out.SetupCode != token {
		t.Fatalf("setup_code %q is not the token in the admin extras %q: the code a parent types "+
			"and the code the QR carries are different secrets", out.SetupCode, token)
	}
	h.enrollDevice(token, "Pixel 8", "Android 15", nil)
}

// The FR-1.8 half, from the server's side: a phone the console revoked can come back with the new
// code and no factory reset. The DPC's own guard against re-enrolling is covered by
// `EnrollerTest`; what has to be true here is that the backend does not care that this device_id
// was enrolled once already, and that the credential it hands back is a working one.
//
// Note what is NOT claimed: this proves the exchange works, not that the phone in the field can
// perform it. A build without the re-link screen cannot, and that is why the console's
// confirmation says so.
func TestARevokedPhoneCanReLinkWithoutAFactoryReset(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	var out provisioningDTO
	h.call(http.MethodPost, "/devices/"+f.device.ID+"/provisioning", f.parent.Token,
		map[string]any{"replace_enrolled": true}).expect(http.StatusOK).decode(&out)
	if out.SetupCode == "" {
		t.Fatal("the provisioning response carries no setup_code, so there is nothing to type")
	}

	// The phone is dead at this point, which is the state a parent is trying to get out of.
	h.call(http.MethodGet, "/device/policy", f.enroll.DeviceToken, nil).expect(http.StatusUnauthorized)

	relinked := h.enrollDevice(out.SetupCode, "Pixel 8", "Android 15", nil)
	if relinked.DeviceToken == f.enroll.DeviceToken {
		t.Fatal("re-linking handed back the same device token that was just revoked")
	}
	h.call(http.MethodGet, "/device/policy", relinked.DeviceToken, nil).expect(http.StatusOK)

	// It is the same device row, not a second phone in the console: the whole point is that the
	// parent does not lose the device's history, its name, or the child it belongs to.
	if relinked.DeviceID != f.device.ID {
		t.Fatalf("re-linking produced device %s, not the one that was revoked (%s)",
			relinked.DeviceID, f.device.ID)
	}
	var view deviceViewDTO
	h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).
		expect(http.StatusOK).decode(&view)
	if !view.Enrolled {
		t.Fatal("the console still reads the re-linked phone as not enrolled")
	}

	// The old credential stays dead. A re-link that left the revoked token working would mean a
	// phone a parent deliberately cut off is still under management.
	h.call(http.MethodGet, "/device/policy", f.enroll.DeviceToken, nil).expect(http.StatusUnauthorized)
}

// A device that has never enrolled must still get its QR with no ceremony, because that is the
// normal path and a guard that blocked it would make the product unusable rather than safe.
func TestAFirstSetupCodeNeedsNoAcknowledgement(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")

	h.call(http.MethodPost, "/devices/"+device.ID+"/provisioning", parent.Token, nil).
		expect(http.StatusOK)
}
