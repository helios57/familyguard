package provisioning

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The signature checksum and the enrollment token are BUILT, not written out as literals.
//
// What these fixtures need is LENGTH: both are 43 characters of url-safe base64 because that is
// what the server actually produces, and the QR payload has to be exercised at the byte count a
// real one carries. What they never needed is ENTROPY — and a 43-character random-looking string
// in a source file is a finding for every secret scanner that exists. The earlier fixtures were
// realistic values kept quiet by a `.gitleaksignore` entry pinned to `commit:file:rule:line`, and
// such a pin is void the moment history is rewritten: the finding comes back on a repository whose
// first CI run is now public. Generating the values keeps the property the test depends on and
// drops the one it never used, so there is nothing to allowlist and nothing to re-pin.
const fixtureB64Len = 43

var (
	fixtureChecksum = strings.Repeat("checksum", 6)[:fixtureB64Len]
	fixtureToken    = strings.Repeat("enrolltok", 5)[:fixtureB64Len]
)

func goodParams() Params {
	return Params{
		Component:         "io.github.helios57.familyguard/.admin.AdminReceiver",
		APKURL:            "https://guard.example.com/dpc.apk",
		SignatureChecksum: fixtureChecksum,
		ServerURL:         "https://guard.example.com",
		EnrollmentToken:   fixtureToken,
		DeviceID:          "6e7cbd1a-2a02-4f8f-a2f9-1c2c4f7c9f11",
		LeaveSystemAppsOn: true,
	}
}

// TestExtraNamesAreExactlyAndroidsSpelling writes the wire names out as literals, deliberately
// duplicating the constants rather than referencing them.
//
// This was added after calibration. Misspelling ExtraLeaveSystemApps by one character left every
// other test in this file GREEN, because they all reach the same constant the payload builder
// reaches — a guard comparing a value to itself. Android ignores an unknown extra in silence, so
// the only symptom of the typo is a provisioned phone with its OEM dialer disabled. The literal
// below is the independent second statement that makes the comparison real; if it ever needs
// changing, that change is the review.
func TestExtraNamesAreExactlyAndroidsSpelling(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{ExtraComponent, "android.app.extra.PROVISIONING_DEVICE_ADMIN_COMPONENT_NAME"},
		{ExtraDownloadLocation, "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_DOWNLOAD_LOCATION"},
		{ExtraSignatureChecksum, "android.app.extra.PROVISIONING_DEVICE_ADMIN_SIGNATURE_CHECKSUM"},
		{ExtraPackageChecksum, "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_CHECKSUM"},
		{ExtraAdminExtras, "android.app.extra.PROVISIONING_ADMIN_EXTRAS_BUNDLE"},
		{ExtraSkipEncryption, "android.app.extra.PROVISIONING_SKIP_ENCRYPTION"},
		{ExtraLeaveSystemApps, "android.app.extra.PROVISIONING_LEAVE_ALL_SYSTEM_APPS_ENABLED"},
		{ExtraWifiSSID, "android.app.extra.PROVISIONING_WIFI_SSID"},
		{ExtraWifiPassword, "android.app.extra.PROVISIONING_WIFI_PASSWORD"},
		{ExtraWifiSecurityType, "android.app.extra.PROVISIONING_WIFI_SECURITY_TYPE"},
	} {
		if c.got != c.want {
			t.Errorf("extra name is %q, Android reads %q", c.got, c.want)
		}
	}
}

// TestPayloadCarriesTheExtrasAndroidActuallyReads. Every one of these keys is ignored in silence
// if misspelled: the setup wizard provisions anyway, and the device comes up without the setting.
func TestPayloadCarriesTheExtrasAndroidActuallyReads(t *testing.T) {
	got, err := Payload(goodParams())
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}

	for _, k := range []string{ExtraComponent, ExtraDownloadLocation, ExtraSignatureChecksum, ExtraAdminExtras} {
		if _, ok := got[k]; !ok {
			t.Errorf("payload is missing %s", k)
		}
	}
	for k := range got {
		if !strings.HasPrefix(k, "android.app.extra.PROVISIONING_") {
			t.Errorf("payload carries %q, which is not a provisioning extra", k)
		}
	}

	admin, ok := got[ExtraAdminExtras].(map[string]any)
	if !ok {
		t.Fatalf("admin extras is %T, not a bundle", got[ExtraAdminExtras])
	}
	if admin[AdminExtraServerURL] != "https://guard.example.com" {
		t.Errorf("server url in bundle = %v", admin[AdminExtraServerURL])
	}
	if admin[AdminExtraEnrollmentToken] == "" || admin[AdminExtraEnrollmentToken] == nil {
		t.Error("the enrollment token did not reach the bundle; the device would provision and never enroll")
	}
	// The package checksum is absent when not supplied, rather than present and empty: an empty
	// string is a checksum Android will compare against and always reject.
	if _, ok := got[ExtraPackageChecksum]; ok {
		t.Error("an empty package checksum was emitted")
	}
}

// TestSystemAppsStayEnabledByRequest guards FR-13: disabling the OEM system apps takes the dialer
// and the emergency shortcut with it on most phones.
func TestSystemAppsStayEnabledByRequest(t *testing.T) {
	p := goodParams()
	got, err := Payload(p)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if got[ExtraLeaveSystemApps] != true {
		t.Error("LEAVE_ALL_SYSTEM_APPS_ENABLED was not set, so provisioning would disable the OEM dialer")
	}
	p.LeaveSystemAppsOn = false
	got, _ = Payload(p)
	if got[ExtraLeaveSystemApps] != false {
		t.Error("the flag is not actually driven by the parameter")
	}
}

// TestRejectedPayloads: each of these produces a device that fails during setup, on the phone,
// with a message that does not name the cause. They are cheaper to catch here.
func TestRejectedPayloads(t *testing.T) {
	cases := map[string]func(*Params){
		"cleartext apk url":     func(p *Params) { p.APKURL = "http://guard.example.com/dpc.apk" },
		"relative apk url":      func(p *Params) { p.APKURL = "/dpc.apk" },
		"cleartext server url":  func(p *Params) { p.ServerURL = "http://guard.example.com" },
		"component without /":   func(p *Params) { p.Component = "io.github.helios57.familyguard.AdminReceiver" },
		"component empty class": func(p *Params) { p.Component = "io.github.helios57.familyguard/" },
		"component empty pkg":   func(p *Params) { p.Component = "/.admin.AdminReceiver" },
		"no checksum at all":    func(p *Params) { p.SignatureChecksum, p.PackageChecksum = "", "" },
		"no enrollment token":   func(p *Params) { p.EnrollmentToken = "" },
		"wifi password no ssid": func(p *Params) { p.WiFiPassword = "hunter2" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := goodParams()
			mutate(&p)
			got, err := Payload(p)
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrInvalidParams) {
				t.Errorf("error %v does not wrap ErrInvalidParams", err)
			}
			if got != nil {
				t.Error("a payload came back alongside the error")
			}
		})
	}
}

// TestLocalhostIsExemptFromHTTPS keeps the end-to-end suite driving the real payload builder
// rather than a mock. A device can never reach localhost, so the exemption cannot widen anything.
func TestLocalhostIsExemptFromHTTPS(t *testing.T) {
	p := goodParams()
	p.ServerURL = "http://127.0.0.1:8080"
	p.APKURL = "http://localhost:8080/dpc.apk"
	if _, err := Payload(p); err != nil {
		t.Fatalf("localhost rejected: %v", err)
	}
	// Negative control: the exemption is by host, not by scheme.
	p.APKURL = "http://guard.example.com/dpc.apk"
	if _, err := Payload(p); err == nil {
		t.Error("the localhost exemption leaked to a real host")
	}
}

// TestChecksumIsComputedFromRealBytes. A checksum carried as configuration is a value nobody
// re-derives when the APK is rebuilt; the mismatch then shows up mid-setup on the phone.
func TestChecksumIsComputedFromRealBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dpc.apk")
	content := []byte("not really an apk, but real bytes")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ChecksumFile(path)
	if err != nil {
		t.Fatalf("ChecksumFile: %v", err)
	}
	sum := sha256.Sum256(content)
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got != want {
		t.Errorf("checksum = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "+/=") {
		t.Errorf("checksum %q is standard base64; Android expects url-safe and unpadded", got)
	}

	// Changing one byte must change the checksum, or the guard is not hashing the content.
	if err := os.WriteFile(path, append(content, '!'), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := ChecksumFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed == got {
		t.Error("the checksum did not change when the file did")
	}

	if _, err := ChecksumFile(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing file produced a checksum")
	}
}

// TestQRHoldsTheRealPayload is the guard against the payload outgrowing what a QR can carry at the
// error-correction level a phone camera needs. It fails by refusing to encode, which is the right
// failure: the alternative is a QR that only scans in perfect light.
func TestQRHoldsTheRealPayload(t *testing.T) {
	payload, err := PayloadJSON(goodParams())
	if err != nil {
		t.Fatalf("PayloadJSON: %v", err)
	}
	t.Logf("payload is %d bytes", len(payload))

	svg, err := QRSVG(payload, 6, 4)
	if err != nil {
		t.Fatalf("QRSVG on a realistic %d-byte payload: %v", len(payload), err)
	}
	if !bytes.HasPrefix(svg, []byte("<svg ")) || !bytes.HasSuffix(svg, []byte("</svg>")) {
		t.Error("output is not a standalone svg document")
	}
	// A strict CSP is only strict if the image needs nothing from outside it. The xmlns is a
	// namespace identifier rather than a fetch, so what is checked is anything that resolves: a
	// reference, a script, an embedded document, or a url() in a style.
	for _, forbidden := range []string{"<script", "href", "<image", "<foreignObject", "url(", "onload", "@import"} {
		if bytes.Contains(svg, []byte(forbidden)) {
			t.Errorf("svg contains %q", forbidden)
		}
	}
	if n := bytes.Count(svg, []byte("M")); n < 100 {
		t.Errorf("svg has %d modules drawn; that is not a QR code", n)
	}

	// Round-trip the JSON so a change that makes the payload unparseable is caught here rather
	// than by a phone.
	var back map[string]any
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	if back[ExtraComponent] != goodParams().Component {
		t.Errorf("component survived as %v", back[ExtraComponent])
	}
}

// TestQRRefusesAnOversizedPayload is the calibration of the guard above, kept as a test: it proves
// the encoder reports failure rather than silently truncating.
func TestQRRefusesAnOversizedPayload(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), 8192)
	if _, err := QRSVG(huge, 6, 4); err == nil {
		t.Fatal("an 8 KB payload encoded successfully, so the size guard measures nothing")
	}
}

// TestQuietZoneIsNeverBelowFour: scanners fail intermittently below four modules, which a parent
// experiences as a bad camera rather than a bad image.
func TestQuietZoneIsNeverBelowFour(t *testing.T) {
	payload, err := PayloadJSON(goodParams())
	if err != nil {
		t.Fatal(err)
	}
	tight, err := QRSVG(payload, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := QRSVG(payload, 6, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tight, wide) {
		t.Error("a quiet zone below four modules was honoured instead of being raised to four")
	}
}
