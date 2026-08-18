// Package provisioning builds the QR payload a factory-reset Android device scans to become a
// fully managed device, and renders it as an image the console can show.
//
// The payload is Android's documented set of PROVISIONING_* extras. Two things about it are easy
// to get wrong and impossible to notice from the server side, so both are enforced here rather
// than left to configuration:
//
//   - Every checksum is computed from bytes on disk. A checksum carried as a configured string is
//     a value nobody re-derives when the APK is rebuilt, and the failure it causes appears on the
//     phone, mid-setup, as "Can't set up device" with no further detail.
//   - The download URL must be https. Android refuses a cleartext download during provisioning,
//     and the refusal likewise surfaces only on the phone.
package provisioning

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"rsc.io/qr"
)

// The extras Android reads out of a provisioning QR code. Spelled out as constants because a typo
// in one of these strings is silently ignored by the setup wizard: the extra is simply absent, and
// the device provisions without the setting.
const (
	ExtraComponent         = "android.app.extra.PROVISIONING_DEVICE_ADMIN_COMPONENT_NAME"
	ExtraDownloadLocation  = "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_DOWNLOAD_LOCATION"
	ExtraSignatureChecksum = "android.app.extra.PROVISIONING_DEVICE_ADMIN_SIGNATURE_CHECKSUM"
	ExtraPackageChecksum   = "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_CHECKSUM"
	ExtraAdminExtras       = "android.app.extra.PROVISIONING_ADMIN_EXTRAS_BUNDLE"
	ExtraSkipEncryption    = "android.app.extra.PROVISIONING_SKIP_ENCRYPTION"
	ExtraLeaveSystemApps   = "android.app.extra.PROVISIONING_LEAVE_ALL_SYSTEM_APPS_ENABLED"
	ExtraWifiSSID          = "android.app.extra.PROVISIONING_WIFI_SSID"
	ExtraWifiPassword      = "android.app.extra.PROVISIONING_WIFI_PASSWORD"
	ExtraWifiSecurityType  = "android.app.extra.PROVISIONING_WIFI_SECURITY_TYPE"
)

// Keys inside the admin extras bundle — the only channel through which the DPC learns which server
// it belongs to and which enrollment it is completing.
const (
	AdminExtraServerURL       = "server_url"
	AdminExtraEnrollmentToken = "enrollment_token"
	AdminExtraDeviceID        = "device_id"
)

// ErrInvalidParams is returned for every rejected payload. Handlers map it to 500 (a
// misconfigured server) rather than 400: none of these values come from the request.
var ErrInvalidParams = errors.New("provisioning: invalid parameters")

// Params is everything that varies between one device's QR and another's.
type Params struct {
	// Component is the DPC's admin receiver, "package/.Receiver".
	Component string
	// APKURL is where the device downloads the DPC. Must be https.
	APKURL string
	// SignatureChecksum is the SHA-256 of the APK signing certificate, url-safe base64. This is
	// what Android 7+ verifies. PackageChecksum, the SHA-256 of the APK file itself, is accepted
	// as an alternative on older releases; at least one must be present.
	SignatureChecksum string
	PackageChecksum   string

	ServerURL       string
	EnrollmentToken string
	DeviceID        string

	// WiFiSSID is optional. A device being set up in a home with no Ethernet needs it to reach the
	// network before it can download anything.
	WiFiSSID          string
	WiFiPassword      string
	WiFiSecurityType  string
	SkipEncryption    bool
	LeaveSystemAppsOn bool
}

// Payload builds the extras map. It validates first and returns nothing on failure, so there is no
// path that produces a half-formed QR a parent would scan and then have to debug on the phone.
func Payload(p Params) (map[string]any, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	admin := map[string]any{
		AdminExtraServerURL:       p.ServerURL,
		AdminExtraEnrollmentToken: p.EnrollmentToken,
	}
	if p.DeviceID != "" {
		admin[AdminExtraDeviceID] = p.DeviceID
	}

	out := map[string]any{
		ExtraComponent:        p.Component,
		ExtraDownloadLocation: p.APKURL,
		ExtraAdminExtras:      admin,
		ExtraSkipEncryption:   p.SkipEncryption,
		// Leaving the system apps enabled is deliberate. The alternative disables everything the
		// OEM shipped, which on most phones includes the dialer and the emergency shortcut — the
		// exact capability FR-13 says must survive every setting.
		ExtraLeaveSystemApps: p.LeaveSystemAppsOn,
	}
	if p.SignatureChecksum != "" {
		out[ExtraSignatureChecksum] = p.SignatureChecksum
	}
	if p.PackageChecksum != "" {
		out[ExtraPackageChecksum] = p.PackageChecksum
	}
	if p.WiFiSSID != "" {
		out[ExtraWifiSSID] = p.WiFiSSID
		if p.WiFiPassword != "" {
			out[ExtraWifiPassword] = p.WiFiPassword
		}
		if p.WiFiSecurityType != "" {
			out[ExtraWifiSecurityType] = p.WiFiSecurityType
		}
	}
	return out, nil
}

func (p Params) validate() error {
	var bad []string

	// "package/.Receiver" or "package/fully.qualified.Receiver". Android parses this with
	// ComponentName.unflattenFromString, which returns null — not an error — for anything else.
	pkg, cls, ok := strings.Cut(p.Component, "/")
	if !ok || pkg == "" || cls == "" || strings.Contains(pkg, "/") {
		bad = append(bad, fmt.Sprintf("component %q is not package/Receiver", p.Component))
	}

	if err := RequireProvisioningURL(p.APKURL); err != nil {
		bad = append(bad, "apk url: "+err.Error())
	}
	if err := RequireProvisioningURL(p.ServerURL); err != nil {
		bad = append(bad, "server url: "+err.Error())
	}
	if p.SignatureChecksum == "" && p.PackageChecksum == "" {
		bad = append(bad, "no checksum: the device would install an unverified APK")
	}
	if p.EnrollmentToken == "" {
		bad = append(bad, "enrollment token is empty: the device would provision and never enroll")
	}
	if p.WiFiPassword != "" && p.WiFiSSID == "" {
		bad = append(bad, "wifi password without an ssid")
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidParams, strings.Join(bad, "; "))
	}
	return nil
}

// RequireProvisioningURL rejects plaintext and non-absolute URLs (FR-1.6: the APK the QR points
// at must be downloadable, unauthenticated, over TLS). Localhost is exempted so the
// end-to-end suite can run the real payload builder against a local server without a certificate;
// a device can never reach localhost anyway, so the exemption cannot weaken a real deployment.
//
// Exported because the configuration loader applies the same rule at startup, and must apply the
// same one. It carried its own copy, and the two had already drifted: config refused every http
// URL outright, so the loopback shape this function explicitly allows was unreachable through it —
// which made the deployment shape where the server hosts its own APK impossible to test end to end,
// and the impossibility read as "that configuration is invalid" rather than "two rules disagree".
func RequireProvisioningURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable: %v", err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("%q is not absolute", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return nil
	}
	return fmt.Errorf("%q is not https; Android refuses a cleartext provisioning download", raw)
}

// PayloadJSON renders the payload exactly as it goes into the QR code.
func PayloadJSON(p Params) ([]byte, error) {
	m, err := Payload(p)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// ChecksumFile is the SHA-256 of a file in the url-safe, unpadded base64 form Android expects.
// The APK's signing certificate and the APK itself are both hashed this way.
func ChecksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

// QRSVG renders the payload as a standalone SVG.
//
// SVG rather than PNG because the console is served under a strict CSP with no inline styles or
// scripts, and because a QR that stays sharp when a parent zooms in on a phone is the difference
// between one scan and five. The image is drawn as one path of square modules; there is no
// external reference of any kind in the output.
func QRSVG(payload []byte, moduleSize, quietModules int) ([]byte, error) {
	if moduleSize <= 0 {
		moduleSize = 6
	}
	if quietModules < 4 {
		// Below four modules of quiet zone, scanners fail intermittently — which reads to a parent
		// as "this phone's camera is bad" rather than as a broken image.
		quietModules = 4
	}
	// Level M survives a phone camera at an angle on a slightly reflective screen. Level L would
	// fit a larger payload, but a QR that only scans head-on is a support call.
	code, err := qr.Encode(string(payload), qr.M)
	if err != nil {
		return nil, fmt.Errorf("provisioning: encode qr (%d bytes): %w", len(payload), err)
	}

	n := code.Size
	dim := (n + 2*quietModules) * moduleSize
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="Provisioning QR code">`,
		dim, dim, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/><path fill="#000000" d="`, dim, dim)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !code.Black(x, y) {
				continue
			}
			fmt.Fprintf(&b, "M%d %dh%dv%dh-%dz",
				(x+quietModules)*moduleSize, (y+quietModules)*moduleSize,
				moduleSize, moduleSize, moduleSize)
		}
	}
	b.WriteString(`"/></svg>`)
	return []byte(b.String()), nil
}
