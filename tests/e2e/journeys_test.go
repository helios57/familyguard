package e2e

// The journeys: what a parent and a phone actually do, end to end, against the real binary.
//
// Every fact asserted here is one an outside client can observe. Nothing reaches into the server,
// and nothing is imported from it — the field names, the status codes and the error codes are
// written out again in this file, so that renaming one on the server is a failure here rather than
// a silent agreement.

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- the shapes a client sees ---------------------------------------------

type familyDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type childDTO struct {
	ID        string `json:"id"`
	FamilyID  string `json:"family_id"`
	Name      string `json:"name"`
	BirthYear *int   `json:"birth_year"`
	CreatedAt string `json:"created_at"`
}

type deviceDTO struct {
	ID               string     `json:"id"`
	ChildID          string     `json:"child_id"`
	Name             string     `json:"name"`
	Model            string     `json:"model"`
	OSVersion        string     `json:"os_version"`
	Locked           bool       `json:"locked"`
	CriticalPackages []string   `json:"critical_packages"`
	EnrolledAt       *time.Time `json:"enrolled_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type deviceStateDTO struct {
	DeviceID      string     `json:"device_id"`
	BatteryLevel  *int       `json:"battery_level"`
	Charging      *bool      `json:"charging"`
	ScreenOn      *bool      `json:"screen_on"`
	Connectivity  string     `json:"connectivity"`
	PolicyVersion int64      `json:"policy_version"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	Online        bool       `json:"online"`

	AppVersionName string `json:"app_version_name"`
	AppVersionCode int64  `json:"app_version_code"`

	// Three-valued (FR-3.6): nil is a phone that has not said, false is one that measured that it
	// cannot see usage at all. A bool here would make those the same answer.
	UsageAccess *bool `json:"usage_access"`
}

type deviceViewDTO struct {
	Device   deviceDTO       `json:"device"`
	State    *deviceStateDTO `json:"state"`
	Enrolled bool            `json:"enrolled"`
}

type policyDTO struct {
	ChildID            string `json:"child_id"`
	TrackingOnly       bool   `json:"tracking_only"`
	AllowChildInstalls bool   `json:"allow_child_installs"`
	AllowDebugging     bool   `json:"allow_debugging"`
	YouTubeBlocked     bool   `json:"youtube_blocked"`
	DailyLimitMinutes  int    `json:"daily_limit_minutes"`
	BedtimeEnabled     bool   `json:"bedtime_enabled"`
	BedtimeStart       string `json:"bedtime_start"`
	BedtimeEnd         string `json:"bedtime_end"`
	DNSHost            string `json:"dns_host"`
	Timezone           string `json:"timezone"`
	Version            int64  `json:"version"`
}

type desiredStateDTO struct {
	Locked                bool     `json:"locked"`
	SuspendReason         string   `json:"suspend_reason"`
	SuspendedPackages     []string `json:"suspended_packages"`
	HiddenPackages        []string `json:"hidden_packages"`
	PendingApproval       []string `json:"pending_approval"`
	PrivateDNSHost        string   `json:"private_dns_host"`
	BlockedDomains        []string `json:"blocked_domains"`
	SafeSearch            bool     `json:"safe_search"`
	YouTubeRestrictedMode bool     `json:"youtube_restricted_mode"`
	AllowInstalls         bool     `json:"allow_installs"`
	UserRestrictions      []string `json:"user_restrictions"`
	QuotaMinutes          int      `json:"quota_minutes"`
	UsedMinutes           int      `json:"used_minutes"`
	RemainingMinutes      int      `json:"remaining_minutes"`
	NextChangeAt          string   `json:"next_change_at"`
	PolicyVersion         int64    `json:"policy_version"`
	// FR-16: the applications this child's phone is declared to have, and everything it needs to
	// fetch and verify each one. See apps_test.go.
	ManagedApps []managedAppEntryDTO `json:"managed_apps"`
}

// managedAppEntryDTO is one entry of that set as the DEVICE receives it — deliberately a different
// shape from the console's managedAppDTO, which joins a declaration to catalog metadata a phone has
// no use for.
type managedAppEntryDTO struct {
	PackageName string `json:"package_name"`
	VersionCode int64  `json:"version_code"`
	VersionName string `json:"version_name"`
	Checksum    string `json:"checksum"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

// policyInputDTO is the second half of the policy response: what the device needs in order to
// recompute the same answer with no network (NFR-3's offline half). A device that received only the
// output would have to guess at 21:00 in a tunnel.
type policyInputDTO struct {
	Settings struct {
		TrackingOnly       bool     `json:"tracking_only"`
		AllowChildInstalls bool     `json:"allow_child_installs"`
		YouTubeBlocked     bool     `json:"youtube_blocked"`
		DailyLimitMinutes  int      `json:"daily_limit_minutes"`
		BedtimeEnabled     bool     `json:"bedtime_enabled"`
		BedtimeStart       string   `json:"bedtime_start"`
		BedtimeEnd         string   `json:"bedtime_end"`
		DNSHost            string   `json:"dns_host"`
		Timezone           string   `json:"timezone"`
		Version            int64    `json:"version"`
		BlockedPackages    []string `json:"blocked_packages"`
		AllowedPackages    []string `json:"allowed_packages"`
		BlockedDomains     []string `json:"blocked_domains"`
		// FR-18, and it is in the INPUT rather than only the output because the phone recomputes
		// this offline. A device given only the computed answer reveals the whole list the first
		// time it loses the network.
		FamilyBlockedPackages []string `json:"family_blocked_packages"`
	} `json:"settings"`
	Installed []struct {
		Package            string `json:"package"`
		System             bool   `json:"system"`
		NewSinceEnrollment bool   `json:"new_since_enrollment"`
	} `json:"installed"`
	UsedMinutesToday int      `json:"used_minutes_today"`
	ParentLock       bool     `json:"parent_lock"`
	CriticalPackages []string `json:"critical_packages"`
	Now              string   `json:"now"`
}

type policyResponseDTO struct {
	Desired desiredStateDTO `json:"desired"`
	Input   policyInputDTO  `json:"input"`
}

type commandDTO struct {
	ID          string         `json:"id"`
	DeviceID    string         `json:"device_id"`
	Type        string         `json:"type"`
	Params      map[string]any `json:"params"`
	State       string         `json:"state"`
	IssuedBy    *string        `json:"issued_by"`
	Result      map[string]any `json:"result"`
	Error       string         `json:"error"`
	DeliveredAt *time.Time     `json:"delivered_at"`
	AckedAt     *time.Time     `json:"acked_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type enrollmentDTO struct {
	DeviceToken string `json:"device_token"`
	DeviceID    string `json:"device_id"`
	ChildID     string `json:"child_id"`
	Recovery    struct {
		Salt       string `json:"salt"`
		Iterations int    `json:"iterations"`
		Hash       string `json:"hash"`
	} `json:"recovery"`
}

type provisioningDTO struct {
	Device  deviceDTO      `json:"device"`
	Payload map[string]any `json:"payload"`
	SVG     string         `json:"svg"`
	// The enrollment token in type-able form (FR-1.8), for the phone that is already a device
	// owner and therefore has no welcome screen left to scan the QR with.
	SetupCode string    `json:"setup_code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type auditEntryDTO struct {
	ID         int64          `json:"id"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// The Android provisioning extras, spelled out here rather than imported. A typo in one of these on
// the server produces a QR that a phone silently ignores; the only way this suite can catch that is
// by carrying its own copy of the strings.
const (
	extraComponent         = "android.app.extra.PROVISIONING_DEVICE_ADMIN_COMPONENT_NAME"
	extraDownloadLocation  = "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_DOWNLOAD_LOCATION"
	extraSignatureChecksum = "android.app.extra.PROVISIONING_DEVICE_ADMIN_SIGNATURE_CHECKSUM"
	extraPackageChecksum   = "android.app.extra.PROVISIONING_DEVICE_ADMIN_PACKAGE_CHECKSUM"
	extraAdminExtras       = "android.app.extra.PROVISIONING_ADMIN_EXTRAS_BUNDLE"
	extraLeaveSystemApps   = "android.app.extra.PROVISIONING_LEAVE_ALL_SYSTEM_APPS_ENABLED"
	extraSkipEncryption    = "android.app.extra.PROVISIONING_SKIP_ENCRYPTION"
)

// The packages the fixtures use. oemDialer is what a device reports as its own dialer: the server
// must widen the critical whitelist with it and can never narrow it (FR-5.5).
const (
	pkgGame      = "com.example.game"
	pkgChat      = "com.example.chat"
	pkgYouTube   = "com.google.android.youtube"
	pkgAOSPPhone = "com.android.dialer"
	oemDialer    = "com.oem.dialer"
)

// ---- fixtures --------------------------------------------------------------

type fixture struct {
	parent sessionDTO
	child  childDTO
	device deviceDTO
	enroll enrollmentDTO
}

// deviceToken is the credential the phone holds after enrolment.
func (f fixture) deviceToken() string { return f.enroll.DeviceToken }

func (h *harness) newChild(token, name string) childDTO {
	h.t.Helper()
	var child childDTO
	h.call(http.MethodPost, "/children", token, map[string]any{"name": name}).
		expect(http.StatusCreated).decode(&child)
	if child.ID == "" {
		h.t.Fatalf("created child %q came back without an id: %+v", name, child)
	}
	return child
}

func (h *harness) newDevice(token, childID, name string) deviceDTO {
	h.t.Helper()
	var dev deviceDTO
	h.call(http.MethodPost, "/children/"+childID+"/devices", token, map[string]any{"name": name}).
		expect(http.StatusCreated).decode(&dev)
	if dev.ID == "" {
		h.t.Fatalf("created device %q came back without an id: %+v", name, dev)
	}
	if dev.EnrolledAt != nil {
		h.t.Fatalf("a device that has never been provisioned reports an enrolment time: %+v", dev)
	}
	return dev
}

// provision mints a QR and returns it. The enrolment token is dug out of the admin extras, which is
// the only channel a real device has for it — reading it from anywhere else would test a path no
// phone uses.
func (h *harness) provision(token, deviceID string) (provisioningDTO, string) {
	return h.mintSetupCode(token, deviceID, false)
}

// reprovision is provision for a device that is ALREADY enrolled, and it is a separate helper
// because the server refuses the plain call there (FR-1.7). The acknowledgement is not ceremony
// the tests can route around: it is the thing that stops a mis-tap revoking a working phone, so a
// test that needs a second code says out loud that it means to replace the first one.
func (h *harness) reprovision(token, deviceID string) (provisioningDTO, string) {
	return h.mintSetupCode(token, deviceID, true)
}

func (h *harness) mintSetupCode(token, deviceID string, replaceEnrolled bool) (provisioningDTO, string) {
	h.t.Helper()
	var body map[string]any
	if replaceEnrolled {
		body = map[string]any{"replace_enrolled": true}
	}
	var out provisioningDTO
	h.call(http.MethodPost, "/devices/"+deviceID+"/provisioning", token, body).
		expect(http.StatusOK).decode(&out)

	admin, ok := out.Payload[extraAdminExtras].(map[string]any)
	if !ok {
		h.t.Fatalf("provisioning payload has no admin extras bundle: %+v", out.Payload)
	}
	enrollToken, _ := admin["enrollment_token"].(string)
	if enrollToken == "" {
		h.t.Fatalf("admin extras carry no enrollment token: %+v", admin)
	}
	return out, enrollToken
}

func (h *harness) enrollDevice(enrollToken, model, osVersion string, critical []string) enrollmentDTO {
	h.t.Helper()
	var out enrollmentDTO
	h.call(http.MethodPost, "/enroll", "", map[string]any{
		"enrollment_token":  enrollToken,
		"model":             model,
		"os_version":        osVersion,
		"critical_packages": critical,
	}).expect(http.StatusOK).decode(&out)
	if out.DeviceToken == "" {
		h.t.Fatal("enrolment returned no device token")
	}
	return out
}

// enrolledFixture is the state most journeys start from: a signed-in parent, a child, and a phone
// that has completed provisioning.
func enrolledFixture(t *testing.T, h *harness) fixture {
	t.Helper()
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})
	if enrolled.DeviceID != device.ID {
		t.Fatalf("enrolment bound the token to device %s, expected %s", enrolled.DeviceID, device.ID)
	}
	if enrolled.ChildID != child.ID {
		t.Fatalf("enrolment reports child %s, expected %s", enrolled.ChildID, child.ID)
	}
	return fixture{parent: parent, child: child, device: device, enroll: enrolled}
}

func (h *harness) patchPolicy(token, childID string, patch map[string]any) policyDTO {
	h.t.Helper()
	var pol policyDTO
	h.call(http.MethodPatch, "/children/"+childID+"/policy", token, patch).
		expect(http.StatusOK).decode(&pol)
	return pol
}

// desiredState asks the console endpoint. at may be "" for now, or an RFC 3339 instant for the
// "what will bedtime do at 21:05?" preview.
func (h *harness) desiredState(token, deviceID, at string) policyResponseDTO {
	h.t.Helper()
	path := "/devices/" + deviceID + "/desired-state"
	if at != "" {
		path += "?at=" + at
	}
	var out policyResponseDTO
	h.call(http.MethodGet, path, token, nil).expect(http.StatusOK).decode(&out)
	return out
}

// devicePolicy asks the same question as the device does, with the device's own credential.
func (h *harness) devicePolicy(deviceToken string) policyResponseDTO {
	h.t.Helper()
	var out policyResponseDTO
	h.call(http.MethodGet, "/device/policy", deviceToken, nil).expect(http.StatusOK).decode(&out)
	return out
}

func (h *harness) issueCommand(token, deviceID, kind string, params map[string]any) commandDTO {
	h.t.Helper()
	body := map[string]any{"type": kind}
	if params != nil {
		body["params"] = params
	}
	var cmd commandDTO
	h.call(http.MethodPost, "/devices/"+deviceID+"/commands", token, body).
		expect(http.StatusAccepted).decode(&cmd)
	return cmd
}

func (h *harness) deviceView(token, deviceID string) deviceViewDTO {
	h.t.Helper()
	var view deviceViewDTO
	h.call(http.MethodGet, "/devices/"+deviceID, token, nil).expect(http.StatusOK).decode(&view)
	return view
}

// ---- small assertions ------------------------------------------------------

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func mustHave(t *testing.T, list []string, want, why string) {
	t.Helper()
	if !has(list, want) {
		t.Fatalf("%s: %q is missing from %v", why, want, list)
	}
}

func mustNotHave(t *testing.T, list []string, unwanted, why string) {
	t.Helper()
	if has(list, unwanted) {
		t.Fatalf("%s: %q must not be in %v", why, unwanted, list)
	}
}

func mustBeSorted(t *testing.T, list []string, what string) {
	t.Helper()
	if !sort.StringsAreSorted(list) {
		// Both engines emit sorted slices so that the shared vectors compare byte for byte. An
		// unsorted one here means the two can disagree while computing the same thing.
		t.Fatalf("%s is not sorted, so the device's engine cannot compare against it: %v", what, list)
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	// Unpadded url-safe, matching what the enroll response documents. Decoding with the strict
	// codec is itself an assertion: standard-alphabet output would fail here.
	out, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q as unpadded url-safe base64: %v", s, err)
	}
	if len(out) == 0 {
		t.Fatal("decoded to zero bytes")
	}
	return out
}

// normalizeCode is the suite's own copy of the normalisation a device performs before deriving the
// hash. Written out rather than imported: if the server changes what it folds, the derived hash
// stops matching here, which is the whole point of checking it from outside.
func normalizeCode(code string) string {
	code = strings.ToUpper(code)
	for _, cut := range []string{"-", " ", "\t"} {
		code = strings.ReplaceAll(code, cut, "")
	}
	return code
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- FR-1, FR-10, FR-9, FR-12, FR-14: the whole device lifecycle -----------

func TestDeviceLifecycleJourney(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	// FR-11.1: the bootstrap list seeded two parents, the first as primary admin.
	if parent.Parent.Role != "PRIMARY_ADMIN" {
		t.Fatalf("the first bootstrap email should be the primary admin, got role %q", parent.Parent.Role)
	}
	second := h.signIn(secondParent)
	if second.Parent.Role != "ADMIN" {
		t.Fatalf("the second bootstrap email should be a plain admin, got role %q", second.Parent.Role)
	}

	var fam familyDTO
	h.call(http.MethodGet, "/family", parent.Token, nil).expect(http.StatusOK).decode(&fam)
	if fam.Name != "E2E Family" {
		t.Fatalf("family name is %q, expected the configured %q", fam.Name, "E2E Family")
	}

	// A parent listing must not leak the Google subject or the FRP account: both identify the
	// adult's Google identity, and neither is anything a console needs.
	parentsBody := h.call(http.MethodGet, "/parents", parent.Token, nil).expect(http.StatusOK)
	var parentList struct {
		Parents []parentDTO `json:"parents"`
	}
	parentsBody.decode(&parentList)
	if len(parentList.Parents) != 2 {
		t.Fatalf("expected the two bootstrap parents, got %d", len(parentList.Parents))
	}
	for _, leak := range []string{"google_sub", "frp_account"} {
		if strings.Contains(string(parentsBody.Body), leak) {
			t.Fatalf("the parent listing exposes %q: %s", leak, parentsBody.Body)
		}
	}
	for _, p := range parentList.Parents {
		if p.GoogleSub != "" || p.FRPAccount != "" {
			t.Fatalf("parent %s carries credential-adjacent identifiers: %+v", p.Email, p)
		}
		if p.LastLoginAt == "" {
			t.Fatalf("parent %s signed in but carries no last_login_at", p.Email)
		}
	}

	// ---- FR-1.1: a child, a device, a QR ----
	child := h.newChild(parent.Token, "Mira")
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")

	// FR-9.2 read the other way round: a command for a device that never enrolled would be a row
	// nothing can ever fetch, and a console that says "sent".
	h.call(http.MethodPost, "/devices/"+device.ID+"/commands", parent.Token,
		map[string]any{"type": "LOCK_NOW"}).expectError(http.StatusConflict, "conflict")

	prov, enrollToken := h.provision(parent.Token, device.ID)

	// FR-1.2: the extras Android actually reads.
	if got := prov.Payload[extraComponent]; got != "io.github.helios57.familyguard/.admin.AdminReceiver" {
		t.Fatalf("provisioning component is %v", got)
	}
	if got := prov.Payload[extraDownloadLocation]; got != "https://apk.example.test/familyguard.apk" {
		t.Fatalf("download location is %v", got)
	}
	if got, ok := prov.Payload[extraLeaveSystemApps].(bool); !ok || !got {
		// Disabling the OEM's system apps takes the dialer and the emergency shortcut with it.
		t.Fatalf("LEAVE_ALL_SYSTEM_APPS_ENABLED is %v, must be true (FR-13/NFR-6)", prov.Payload[extraLeaveSystemApps])
	}
	if _, ok := prov.Payload[extraSkipEncryption].(bool); !ok {
		t.Fatalf("SKIP_ENCRYPTION is absent or not a boolean: %v", prov.Payload[extraSkipEncryption])
	}

	// FR-1.3: the checksum is the SHA-256 of the certificate bytes on disk, url-safe base64, no
	// padding. The expected value is computed by this test from the same bytes it wrote, so what is
	// being checked is the encoding — not a constant copied out of the server.
	if got := prov.Payload[extraSignatureChecksum]; got != h.apkCertSum {
		t.Fatalf("signature checksum is %v, expected %s", got, h.apkCertSum)
	}
	if strings.ContainsAny(h.apkCertSum, "=+/") {
		t.Fatalf("the checksum encoding is not url-safe and unpadded: %q", h.apkCertSum)
	}
	admin := prov.Payload[extraAdminExtras].(map[string]any)
	if admin["server_url"] != h.base {
		t.Fatalf("admin extras point at %v, this server is %s", admin["server_url"], h.base)
	}
	if admin["device_id"] != device.ID {
		t.Fatalf("admin extras name device %v, expected %s", admin["device_id"], device.ID)
	}

	// The QR itself: an SVG with no external reference of any kind, which is what lets it render
	// under the console's CSP.
	if !strings.HasPrefix(prov.SVG, "<svg") || !strings.Contains(prov.SVG, "<path") {
		t.Fatalf("provisioning svg does not look like a QR: %.120s", prov.SVG)
	}
	for _, forbidden := range []string{"<script", "href=", "xlink", "<image"} {
		if strings.Contains(prov.SVG, forbidden) {
			t.Fatalf("provisioning svg contains %q, which a strict CSP will refuse", forbidden)
		}
	}
	if d := time.Until(prov.ExpiresAt); d <= 25*time.Minute || d > 31*time.Minute {
		t.Fatalf("the enrolment window is %s from now; expected about 30 minutes", d)
	}

	// ---- FR-1.4: single-use enrolment ----
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})
	h.call(http.MethodPost, "/enroll", "", map[string]any{"enrollment_token": enrollToken}).
		expectError(http.StatusConflict, "conflict")

	// FR-12.3: the device receives material to check a code offline, never the code itself.
	if strings.Contains(string(h.call(http.MethodPost, "/enroll", "", map[string]any{
		"enrollment_token": "nonsense"}).Body), "recovery") {
		t.Fatal("a refused enrolment carries recovery material")
	}
	if enrolled.Recovery.Iterations != 120000 {
		t.Fatalf("recovery iterations are %d; the DPC and the server must agree on the work factor",
			enrolled.Recovery.Iterations)
	}

	// ---- FR-12.3 continued: the console's code and the device's material must agree ----
	var codeBody struct {
		DeviceID string `json:"device_id"`
		Code     string `json:"recovery_code"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/recovery-code", parent.Token, nil).
		expect(http.StatusOK).decode(&codeBody)
	if codeBody.Code == "" {
		t.Fatal("the console shows an empty recovery code")
	}
	// Derived here, independently, with the stdlib rather than with the server's helper. A test that
	// called the server's own derivation would agree with any bug in it.
	salt := mustB64(t, enrolled.Recovery.Salt)
	want := mustB64(t, enrolled.Recovery.Hash)
	got, err := pbkdf2.Key(sha256.New, normalizeCode(codeBody.Code), salt, enrolled.Recovery.Iterations, len(want))
	if err != nil {
		t.Fatalf("derive the recovery hash: %v", err)
	}
	if string(got) != string(want) {
		t.Fatal("the code the console shows does not derive the hash the device was given: " +
			"a parent reading it out to an offline phone would be told it is wrong")
	}
	// The negative half. Without it, a derivation that returned a constant would pass the line above.
	wrong, err := pbkdf2.Key(sha256.New, normalizeCode(codeBody.Code)+"X", salt, enrolled.Recovery.Iterations, len(want))
	if err != nil {
		t.Fatalf("derive the control hash: %v", err)
	}
	if string(wrong) == string(want) {
		t.Fatal("a different code derives the same hash, so the comparison proves nothing")
	}

	// ---- FR-10: telemetry ----
	battery := 71
	var beat struct {
		PolicyVersion   int64  `json:"policy_version"`
		PendingCommands int    `json:"pending_commands"`
		Locked          bool   `json:"locked"`
		ServerTime      string `json:"server_time"`
	}
	h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken, map[string]any{
		"battery_level": battery, "charging": true, "screen_on": false,
		"connectivity": "wifi", "policy_version": 0,
	}).expect(http.StatusOK).decode(&beat)
	if beat.PendingCommands != 0 || beat.Locked {
		t.Fatalf("a freshly enrolled device reports %+v", beat)
	}
	if _, err := time.Parse(time.RFC3339, beat.ServerTime); err != nil {
		t.Fatalf("server_time %q is not RFC 3339: %v", beat.ServerTime, err)
	}

	view := h.deviceView(parent.Token, device.ID)
	if !view.Enrolled || view.Device.EnrolledAt == nil {
		t.Fatalf("the console still shows the device as unenrolled: %+v", view)
	}
	if view.State == nil {
		t.Fatal("the device reported a heartbeat and the console has no state for it")
	}
	if view.State.BatteryLevel == nil || *view.State.BatteryLevel != battery {
		t.Fatalf("battery came back as %v", view.State.BatteryLevel)
	}
	if !view.State.Online {
		t.Fatalf("a device that just checked in is shown offline: %+v", view.State)
	}
	if view.Device.Model != "Pixel 7a" || view.Device.OSVersion != "Android 14" {
		t.Fatalf("hardware details did not survive enrolment: %+v", view.Device)
	}
	mustHave(t, view.Device.CriticalPackages, oemDialer,
		"the device's own dialer must reach the server's whitelist")

	// ---- FR-5.1: inventory ----
	var inv struct {
		Apps int `json:"apps"`
	}
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Game", "system_app": false},
			{"package_name": pkgChat, "label": "Chat", "system_app": false},
			{"package_name": pkgYouTube, "label": "YouTube", "system_app": false},
			{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
			{"package_name": oemDialer, "label": "Dialer", "system_app": true},
			{"package_name": "   ", "label": "blank", "system_app": false},
		},
	}).expect(http.StatusOK).decode(&inv)
	if inv.Apps != 5 {
		t.Fatalf("inventory accepted %d apps; the blank package name should have been dropped", inv.Apps)
	}
	var appList struct {
		Apps []struct {
			PackageName string `json:"package_name"`
			Label       string `json:"label"`
			SystemApp   bool   `json:"system_app"`
		} `json:"apps"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/apps?include_system=1", parent.Token, nil).
		expect(http.StatusOK).decode(&appList)
	if len(appList.Apps) != 5 {
		t.Fatalf("the console lists %d apps, the device reported 5", len(appList.Apps))
	}

	// ---- FR-3.1, FR-3.5: usage ----
	var usageAck struct {
		Day     string `json:"day"`
		Minutes int    `json:"minutes"`
	}
	h.call(http.MethodPost, "/device/usage", enrolled.DeviceToken, map[string]any{
		"samples": map[string]int64{pkgGame: 45 * 60 * 1000, pkgChat: 15 * 60 * 1000},
	}).expect(http.StatusOK).decode(&usageAck)
	if usageAck.Minutes != 60 {
		t.Fatalf("60 minutes of samples came back as %d", usageAck.Minutes)
	}

	var usage struct {
		Day      string `json:"day"`
		Timezone string `json:"timezone"`
		Minutes  int    `json:"minutes"`
		Packages []struct {
			PackageName  string `json:"package_name"`
			ForegroundMs int64  `json:"foreground_ms"`
		} `json:"packages"`
		History []struct {
			Day     string `json:"day"`
			Minutes int    `json:"minutes"`
		} `json:"history"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/usage", parent.Token, nil).
		expect(http.StatusOK).decode(&usage)
	if usage.Minutes != 60 || usage.Day != usageAck.Day {
		t.Fatalf("the console reports %d minutes on %s, the device filed %d on %s",
			usage.Minutes, usage.Day, usageAck.Minutes, usageAck.Day)
	}
	if usage.Timezone == "" {
		t.Fatal("usage is reported without the zone its day boundary was computed in")
	}
	if len(usage.Packages) != 2 {
		t.Fatalf("per-app usage lists %d packages, expected 2 (FR-3.5)", len(usage.Packages))
	}
	if len(usage.History) == 0 {
		t.Fatal("usage history is empty on a day that has usage")
	}

	// A second report for the same day replaces rather than accumulates: the device reports totals,
	// so adding them would double every figure on the second heartbeat of the day.
	h.call(http.MethodPost, "/device/usage", enrolled.DeviceToken, map[string]any{
		"samples": map[string]int64{pkgGame: 45 * 60 * 1000, pkgChat: 15 * 60 * 1000},
	}).expect(http.StatusOK).decode(&usageAck)
	if usageAck.Minutes != 60 {
		t.Fatalf("re-reporting the same totals made the day %d minutes long", usageAck.Minutes)
	}

	// ---- FR-9/FR-11.3: location ----
	var loc struct {
		ID         string   `json:"id"`
		DeviceID   string   `json:"device_id"`
		Latitude   float64  `json:"latitude"`
		Longitude  float64  `json:"longitude"`
		AccuracyM  *float64 `json:"accuracy_m"`
		CapturedAt string   `json:"captured_at"`
	}
	h.call(http.MethodPost, "/device/location", enrolled.DeviceToken, map[string]any{
		"latitude": 47.3769, "longitude": 8.5417, "accuracy_m": 12.5,
	}).expect(http.StatusOK).decode(&loc)
	if loc.DeviceID != device.ID {
		t.Fatalf("the fix was filed against device %s", loc.DeviceID)
	}
	var locList struct {
		Locations []struct {
			Latitude float64 `json:"latitude"`
		} `json:"locations"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/locations", parent.Token, nil).
		expect(http.StatusOK).decode(&locList)
	if len(locList.Locations) != 1 {
		t.Fatalf("the console shows %d fixes, one was reported", len(locList.Locations))
	}

	// ---- FR-9.1/FR-9.3/NFR-3: a command is delivered when it is fetched, never when it is sent ----
	cmd := h.issueCommand(parent.Token, device.ID, "LOCK_NOW", nil)
	if cmd.State != "QUEUED" {
		t.Fatalf("a freshly issued command is already %q; issuing is not delivering (NFR-3)", cmd.State)
	}
	if cmd.IssuedBy == nil || *cmd.IssuedBy != parent.Parent.ID {
		t.Fatalf("the command records issuer %v, expected %s", cmd.IssuedBy, parent.Parent.ID)
	}

	// FR-9.1: still queued as far as the console is concerned, because nothing has fetched it.
	if state := h.commandState(parent.Token, device.ID, cmd.ID); state != "QUEUED" {
		t.Fatalf("the console reports %q before the device fetched anything", state)
	}
	// The heartbeat is the certain path: it tells a device with no stream that it is behind.
	h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken,
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK).decode(&beat)
	if beat.PendingCommands != 1 {
		t.Fatalf("heartbeat reports %d pending commands, one is queued", beat.PendingCommands)
	}
	if !beat.Locked {
		t.Fatal("LOCK_NOW did not set durable lock state; a reboot would undo the parent's lock (FR-9.1)")
	}

	var fetched struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/device/commands", enrolled.DeviceToken, nil).
		expect(http.StatusOK).decode(&fetched)
	if len(fetched.Commands) != 1 || fetched.Commands[0].ID != cmd.ID {
		t.Fatalf("the device fetched %+v", fetched.Commands)
	}
	if fetched.Commands[0].State != "DELIVERED" || fetched.Commands[0].DeliveredAt == nil {
		t.Fatalf("a fetched command is %q with delivered_at %v",
			fetched.Commands[0].State, fetched.Commands[0].DeliveredAt)
	}
	if state := h.commandState(parent.Token, device.ID, cmd.ID); state != "DELIVERED" {
		t.Fatalf("the console reports %q after the device fetched it", state)
	}
	// A second fetch returns nothing: delivered is not pending, or every reconnect would replay
	// every alarm the phone ever received.
	h.call(http.MethodGet, "/device/commands", enrolled.DeviceToken, nil).
		expect(http.StatusOK).decode(&fetched)
	if len(fetched.Commands) != 0 {
		t.Fatalf("a delivered command was handed over again: %+v", fetched.Commands)
	}

	var acked commandDTO
	h.call(http.MethodPost, "/device/commands/"+cmd.ID+"/ack", enrolled.DeviceToken,
		map[string]any{"ok": true, "result": map[string]any{"locked": true}}).
		expect(http.StatusOK).decode(&acked)
	if acked.State != "ACKED" || acked.AckedAt == nil {
		t.Fatalf("acknowledging left the command %q: %+v", acked.State, acked)
	}
	if state := h.commandState(parent.Token, device.ID, cmd.ID); state != "ACKED" {
		t.Fatalf("the console reports %q after the device acknowledged", state)
	}

	// FR-9: the lock is reversible, and the reversal is durable too.
	h.issueCommand(parent.Token, device.ID, "UNLOCK_DEVICE", nil)
	if view := h.deviceView(parent.Token, device.ID); view.Device.Locked {
		t.Fatal("UNLOCK_DEVICE left the device locked; no policy state may be one-way (FR-4.2)")
	}

	// A failed acknowledgement is recorded as failed, not quietly dropped (FR-9.3).
	alarm := h.issueCommand(parent.Token, device.ID, "TRIGGER_ALARM", map[string]any{"seconds": 30})
	h.call(http.MethodGet, "/device/commands", enrolled.DeviceToken, nil).expect(http.StatusOK)
	var failed commandDTO
	h.call(http.MethodPost, "/device/commands/"+alarm.ID+"/ack", enrolled.DeviceToken,
		map[string]any{"ok": false, "error": "audio focus denied"}).
		expect(http.StatusOK).decode(&failed)
	if failed.State != "FAILED" || failed.Error != "audio focus denied" {
		t.Fatalf("a refused command came back as %q / %q", failed.State, failed.Error)
	}

	// ---- FR-12.5: recovery attempts reach the console ----
	h.call(http.MethodPost, "/device/recovery-event", enrolled.DeviceToken,
		map[string]any{"succeeded": false}).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/recovery-event", enrolled.DeviceToken,
		map[string]any{"succeeded": true}).expect(http.StatusOK)
	var events struct {
		Events []struct {
			Succeeded  bool      `json:"succeeded"`
			OccurredAt time.Time `json:"occurred_at"`
		} `json:"events"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/recovery-events", parent.Token, nil).
		expect(http.StatusOK).decode(&events)
	if len(events.Events) != 2 {
		t.Fatalf("the console shows %d recovery attempts, two were reported (FR-12.5)", len(events.Events))
	}
	failures := 0
	for _, e := range events.Events {
		if !e.Succeeded {
			failures++
		}
	}
	if failures != 1 {
		// A console that only showed successes could not distinguish a mistyped code from a child
		// working through the keyspace.
		t.Fatalf("failed attempts are not visible: %+v", events.Events)
	}

	// ---- FR-14: the audit log ----
	var audit struct {
		Entries []auditEntryDTO `json:"entries"`
	}
	h.call(http.MethodGet, "/audit?limit=500", parent.Token, nil).expect(http.StatusOK).decode(&audit)
	seen := map[string]bool{}
	for _, e := range audit.Entries {
		seen[e.Action] = true
		if e.OccurredAt.IsZero() || e.ActorType == "" || e.ActorID == "" {
			t.Fatalf("audit entry is missing actor or time: %+v", e)
		}
	}
	for _, action := range []string{
		"PARENT_SIGNED_IN", "CHILD_ADDED", "DEVICE_ADDED", "ENROLLMENT_ISSUED", "DEVICE_ENROLLED",
		"COMMAND_ISSUED", "COMMAND_ACKED", "COMMAND_FAILED", "RECOVERY_CODE_VIEWED", "RECOVERY_CODE_USED",
	} {
		if !seen[action] {
			t.Fatalf("the audit log has no %s entry; it records %v", action, sortedKeys(seen))
		}
	}
	// The recovery code itself must never be written into the record of who looked at it.
	if strings.Contains(string(mustJSON(t, audit.Entries)), codeBody.Code) {
		t.Fatal("the audit log contains the plaintext recovery code")
	}
}

// commandState reads one command's state back from the console's listing.
func (h *harness) commandState(token, deviceID, commandID string) string {
	h.t.Helper()
	var list struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/devices/"+deviceID+"/commands", token, nil).
		expect(http.StatusOK).decode(&list)
	for _, c := range list.Commands {
		if c.ID == commandID {
			return c.State
		}
	}
	h.t.Fatalf("command %s is not in the console's listing: %+v", commandID, list.Commands)
	return ""
}

// ---- FR-3 … FR-8: what the phone is actually told to do --------------------

func TestPolicyEnforcementJourney(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Game"},
			{"package_name": pkgChat, "label": "Chat"},
			{"package_name": pkgYouTube, "label": "YouTube"},
			{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
			{"package_name": oemDialer, "label": "Dialer", "system_app": true},
		},
	}).expect(http.StatusOK)

	// The defaults, before a parent touches anything.
	//
	// "Nothing suspended" stopped being the right baseline when FR-18 landed: a new child starts
	// out with the curated family blocklist in effect, which is the whole point of it. So the
	// assertion is that nothing beyond that list is restrained — an off-by-one in the engine would
	// still show up here as an app of this child's that should not be suspended.
	base := h.desiredState(f.parent.Token, f.device.ID, "")
	if base.Desired.SuspendReason != "" {
		t.Fatalf("a new child starts out restrained: %+v", base.Desired)
	}
	if extra := suspendedBeyondTheBlocklist(base.Desired); len(extra) != 0 {
		t.Fatalf("a new child starts out with %v suspended, which is not on the curated "+
			"blocklist: %+v", extra, base.Desired)
	}
	// And the curated list is actually there, so the loop above is not vacuously green over an
	// empty slice.
	if len(base.Desired.SuspendedPackages) == 0 {
		t.Fatal("a new child has an empty suspended set; the curated blocklist (FR-18) is not reaching the device")
	}
	// No filtering resolver by default (FR-6.1). This assertion used to read the other way round —
	// it required a resolver to be set on a brand-new child, and the default was AdGuard Family DNS.
	// The owner removed it on 2026-09-06: a filtering resolver does nothing about advertising served
	// from inside an app over the app's own TLS connection, which is the advertising that actually
	// bothers anybody, so it bought a permanently-locked network setting for close to nothing. An
	// empty host is a defined state and NOT "off": Android reads it as OPPORTUNISTIC, so DNS is
	// still encrypted to whatever resolver the network hands out.
	if base.Desired.PrivateDNSHost != "" {
		t.Fatalf("a new child has private DNS pinned to %q; since 2026-09-06 no resolver is "+
			"configured until a parent names one (FR-6.1)", base.Desired.PrivateDNSHost)
	}
	if !base.Desired.SafeSearch || !base.Desired.YouTubeRestrictedMode {
		t.Fatalf("managed-browser policy is not enforced by default (FR-6.3): %+v", base.Desired)
	}
	// FR-2.1 hardening, and the three restrictions that must never appear (NFR-6).
	//
	// `disallow_config_private_dns` is not in this list and is not an omission: since 2026-09-06 no
	// filtering resolver is configured by default, and the lock exists to stop a child undoing a
	// resolver that is set. Both halves of that coupling are measured in
	// TestNoFilteringResolverIsConfiguredByDefault.
	for _, want := range []string{
		"no_safe_boot", "no_debugging_features",
		"no_config_date_time", "no_add_user", "no_install_unknown_sources", "no_uninstall_apps",
	} {
		mustHave(t, base.Desired.UserRestrictions, want, "device-owner hardening (FR-2.1)")
	}
	for _, forbidden := range []string{"no_outgoing_calls", "no_sms", "no_create_windows"} {
		mustNotHave(t, base.Desired.UserRestrictions, forbidden,
			"a phone that cannot call or be reached breaks NFR-6")
	}
	mustBeSorted(t, base.Desired.UserRestrictions, "user_restrictions")
	mustHave(t, base.Input.CriticalPackages, oemDialer, "the device's reported whitelist")

	// The device's own view must be identical to the console's. Two sources of truth here means a
	// parent insisting a setting is on while the phone behaves as though it is off.
	fromDevice := h.devicePolicy(f.deviceToken())
	if string(mustJSON(t, fromDevice.Desired)) != string(mustJSON(t, base.Desired)) {
		t.Fatalf("the console and the device are told different things:\nconsole: %s\ndevice:  %s",
			mustJSON(t, base.Desired), mustJSON(t, fromDevice.Desired))
	}

	// ---- FR-5.2: block an app; it is suspended and hidden ----
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgGame, "action": "BLOCK"}).expect(http.StatusOK)
	blocked := h.desiredState(f.parent.Token, f.device.ID, "")
	mustHave(t, blocked.Desired.SuspendedPackages, pkgGame, "a blocked app is suspended (FR-5.2)")
	mustHave(t, blocked.Desired.HiddenPackages, pkgGame, "a blocked app is hidden (FR-5.2)")
	if blocked.Desired.PolicyVersion <= base.Desired.PolicyVersion {
		// Without the bump the row is unchanged, so a device comparing versions never re-fetches and
		// the parent's block applies only after some unrelated edit.
		t.Fatalf("blocking an app did not raise the policy version (%d → %d)",
			base.Desired.PolicyVersion, blocked.Desired.PolicyVersion)
	}

	// FR-5.2 reversed: removing the rule restores access. No rule store may be one-way.
	h.call(http.MethodDelete, "/children/"+f.child.ID+"/app-rules?package_name="+pkgGame,
		f.parent.Token, nil).expect(http.StatusNoContent)
	unblocked := h.desiredState(f.parent.Token, f.device.ID, "")
	mustNotHave(t, unblocked.Desired.SuspendedPackages, pkgGame, "unblocking must restore the app")
	mustNotHave(t, unblocked.Desired.HiddenPackages, pkgGame, "unblocking must unhide the app")

	// ---- FR-6.2/FR-6.4: custom domains, and their removal ----
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"dns_host": "kids.example-dns.test"})
	h.call(http.MethodPost, "/children/"+f.child.ID+"/blocked-domains", f.parent.Token,
		map[string]any{"domain": "Bad.Example.COM"}).expect(http.StatusCreated)
	withDomain := h.desiredState(f.parent.Token, f.device.ID, "")
	if withDomain.Desired.PrivateDNSHost != "kids.example-dns.test" {
		t.Fatalf("the filtering endpoint is %q (FR-6.2)", withDomain.Desired.PrivateDNSHost)
	}
	mustHave(t, withDomain.Desired.BlockedDomains, "bad.example.com",
		"a custom domain must reach the device, normalised")
	h.call(http.MethodDelete, "/children/"+f.child.ID+"/blocked-domains?domain=bad.example.com",
		f.parent.Token, nil).expect(http.StatusNoContent)
	mustNotHave(t, h.desiredState(f.parent.Token, f.device.ID, "").Desired.BlockedDomains,
		"bad.example.com", "removal must actually restore access (FR-6.4)")

	// ---- FR-7: the YouTube killswitch, driven the way the console does — as a command ----
	h.issueCommand(f.parent.Token, f.device.ID, "BLOCK_YOUTUBE_ALL", nil)
	yt := h.desiredState(f.parent.Token, f.device.ID, "")
	mustHave(t, yt.Desired.SuspendedPackages, pkgYouTube, "FR-7.1 suspends the app family")
	mustHave(t, yt.Desired.HiddenPackages, pkgYouTube, "FR-7.1 hides the app family")
	for _, d := range []string{"youtube.com", "youtu.be", "googlevideo.com"} {
		mustHave(t, yt.Desired.BlockedDomains, d, "FR-7.2 blocks the media domains too")
	}
	// The killswitch is policy, not just a command: it must survive a reinstall and be picked up by
	// a device that was offline when it was thrown.
	var pol policyDTO
	h.call(http.MethodGet, "/children/"+f.child.ID+"/policy", f.parent.Token, nil).
		expect(http.StatusOK).decode(&pol)
	if !pol.YouTubeBlocked {
		t.Fatal("BLOCK_YOUTUBE_ALL did not write the policy, so an offline device would never see it")
	}

	// FR-7.4: the toggle is symmetric.
	h.issueCommand(f.parent.Token, f.device.ID, "UNBLOCK_YOUTUBE_ALL", nil)
	lifted := h.desiredState(f.parent.Token, f.device.ID, "")
	mustNotHave(t, lifted.Desired.SuspendedPackages, pkgYouTube, "FR-7.4 lifts the suspension")
	mustNotHave(t, lifted.Desired.BlockedDomains, "youtube.com", "FR-7.4 lifts the DNS block")

	// ---- FR-3.4: the quota ----
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{pkgGame: 60 * 60 * 1000},
	}).expect(http.StatusOK)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"daily_limit_minutes": 60, "bedtime_enabled": false,
	})
	quota := h.desiredState(f.parent.Token, f.device.ID, "")
	if quota.Desired.SuspendReason != "QUOTA" {
		t.Fatalf("60 minutes used against a 60 minute limit is %q: %+v",
			quota.Desired.SuspendReason, quota.Desired)
	}
	if quota.Desired.UsedMinutes != 60 || quota.Desired.RemainingMinutes != 0 {
		t.Fatalf("quota accounting is %+v", quota.Desired)
	}
	mustHave(t, quota.Desired.SuspendedPackages, pkgGame, "an exhausted quota suspends apps (FR-3.4)")
	// FR-5.5, the invariant the whole design turns on.
	mustNotHave(t, quota.Desired.SuspendedPackages, pkgAOSPPhone, "the dialer is never suspended")
	mustNotHave(t, quota.Desired.SuspendedPackages, oemDialer, "the OEM dialer is never suspended")
	mustNotHave(t, quota.Desired.HiddenPackages, pkgAOSPPhone, "the dialer is never hidden")
	mustNotHave(t, quota.Desired.SuspendedPackages, "io.github.helios57.familyguard",
		"a suspended DPC could not lift its own suspension")
	if quota.Desired.NextChangeAt == "" {
		t.Fatal("a quota is set but nothing says when it resets; the device would have to poll (NFR-10)")
	}
	if _, err := time.Parse(time.RFC3339, quota.Desired.NextChangeAt); err != nil {
		t.Fatalf("next_change_at %q is not RFC 3339: %v", quota.Desired.NextChangeAt, err)
	}

	// An explicit ALLOW is the exemption a parent can grant against a quota.
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgChat, "action": "ALLOW"}).expect(http.StatusOK)
	exempt := h.desiredState(f.parent.Token, f.device.ID, "")
	mustNotHave(t, exempt.Desired.SuspendedPackages, pkgChat, "an allowed app survives the quota")
	mustHave(t, exempt.Desired.SuspendedPackages, pkgGame, "the exemption is per app, not global")

	// Raising the limit lifts it again — the reversibility invariant.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 240})
	if s := h.desiredState(f.parent.Token, f.device.ID, "").Desired; s.SuspendReason != "" {
		t.Fatalf("raising the limit left the child at %q", s.SuspendReason)
	}

	// ---- FR-4: bedtime, previewed rather than waited for ----
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": "21:00", "bedtime_end": "07:00",
		"timezone": "Europe/Zurich", "daily_limit_minutes": 0,
	})
	// Both instants are written with the zone's offset spelled out, so the test does not depend on
	// the machine's clock or on the server's.
	night := h.desiredState(f.parent.Token, f.device.ID, "2026-01-15T22:30:00%2B01:00")
	if night.Desired.SuspendReason != "BEDTIME" {
		t.Fatalf("22:30 inside a 21:00–07:00 window is %q", night.Desired.SuspendReason)
	}
	mustHave(t, night.Desired.SuspendedPackages, pkgGame, "bedtime suspends non-exempt apps (FR-4.2)")
	mustNotHave(t, night.Desired.SuspendedPackages, pkgAOSPPhone, "bedtime never takes the dialer")
	if night.Desired.Locked {
		// A locked keyguard is less able to place an emergency call than suspended apps.
		t.Fatal("bedtime locked the keyguard; only a parent's LOCK_NOW may do that")
	}
	// The window crosses midnight, so the morning side must be inside it and the afternoon outside.
	if s := h.desiredState(f.parent.Token, f.device.ID, "2026-01-16T06:30:00%2B01:00").Desired; s.SuspendReason != "BEDTIME" {
		t.Fatalf("06:30 is on the far side of midnight and reads as %q (FR-4.1)", s.SuspendReason)
	}
	day := h.desiredState(f.parent.Token, f.device.ID, "2026-01-16T14:00:00%2B01:00")
	// The curated family blocklist (FR-18) is a floor that bedtime is not what put there, so
	// leaving the window releases everything except it. Asserting "nothing suspended" would now be
	// asserting that FR-18 does not work.
	if day.Desired.SuspendReason != "" || len(suspendedBeyondTheBlocklist(day.Desired)) != 0 {
		t.Fatalf("leaving the window did not un-suspend (FR-4.2): %+v", day.Desired)
	}

	// ---- FR-5.4: free installation off puts new apps in front of a parent ----
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_child_installs": false})
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Game"},
			{"package_name": pkgChat, "label": "Chat"},
			{"package_name": pkgYouTube, "label": "YouTube"},
			{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
			{"package_name": oemDialer, "label": "Dialer", "system_app": true},
			{"package_name": "com.example.newly.installed", "label": "New"},
		},
	}).expect(http.StatusOK)
	pending := h.desiredState(f.parent.Token, f.device.ID, "2026-01-16T14:00:00%2B01:00")
	mustHave(t, pending.Desired.PendingApproval, "com.example.newly.installed",
		"an app installed after enrolment waits for a parent (FR-5.4)")
	mustHave(t, pending.Desired.SuspendedPackages, "com.example.newly.installed",
		"a pending app is suspended until it is approved")
	mustNotHave(t, pending.Desired.PendingApproval, pkgAOSPPhone,
		"a preinstalled system app is not a new installation")
	// The discriminating half, and the reason this block reports two inventories rather than one.
	// "New" has to mean "appeared after the phone told us what it already had", not "appeared after
	// enrolment" — the first inventory necessarily arrives after enrolment, so measuring novelty
	// against enrolled_at sweeps the entire preinstalled catalogue into the queue the moment a
	// parent turns free installation off. These five were in the FIRST report and none of them is
	// new. Asserting only on the system apps above would miss it: a system_app flag can rescue the
	// dialer while every ordinary preinstalled app is still swept up.
	for _, pkg := range []string{pkgGame, pkgChat, pkgYouTube, oemDialer} {
		mustNotHave(t, pending.Desired.PendingApproval, pkg,
			"an app the device reported in its first inventory is not newly installed (FR-5.4)")
		mustNotHave(t, pending.Desired.SuspendedPackages, pkg,
			"turning free installation off must not suspend apps the child already had")
	}
	if pending.Desired.AllowInstalls {
		t.Fatal("free installation reads as on after being turned off (FR-5.3)")
	}
	mustHave(t, pending.Desired.UserRestrictions, "no_install_apps",
		"free installation off must also stop the install itself")

	// FR-5.4 continued: approving it releases the app, and nothing else.
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": "com.example.newly.installed", "action": "ALLOW"}).
		expect(http.StatusOK)
	approved := h.desiredState(f.parent.Token, f.device.ID, "2026-01-16T14:00:00%2B01:00")
	mustNotHave(t, approved.Desired.PendingApproval, "com.example.newly.installed",
		"approving must clear the queue")
	mustNotHave(t, approved.Desired.SuspendedPackages, "com.example.newly.installed",
		"approving must release the app")

	// ---- FR-8: tracking-only measures without restraining ----
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"tracking_only": true, "allow_child_installs": true,
	})
	tracking := h.desiredState(f.parent.Token, f.device.ID, "2026-01-15T22:30:00%2B01:00")
	if tracking.Desired.SuspendReason != "" || len(tracking.Desired.SuspendedPackages) != 0 {
		t.Fatalf("tracking-only still enforces bedtime: %+v", tracking.Desired)
	}
	// The resolver this journey configured back at FR-6.2 is still in force, and so is the managed
	// browser policy. Both are named, because tracking-only turning off DNS and tracking-only
	// turning off SafeSearch are different bugs with the same shape.
	if tracking.Desired.PrivateDNSHost != "kids.example-dns.test" {
		t.Fatalf("tracking-only dropped the configured resolver, which FR-8 keeps: %+v", tracking.Desired)
	}
	if !tracking.Desired.SafeSearch || !tracking.Desired.YouTubeRestrictedMode {
		t.Fatalf("tracking-only dropped managed-browser filtering, which FR-8 keeps: %+v", tracking.Desired)
	}
	mustHave(t, tracking.Desired.UserRestrictions, "no_debugging_features",
		"hardening remains in effect in tracking-only (FR-8)")

	// A parent's explicit lock is honoured even in tracking-only: whoever presses it is not thinking
	// about the enforcement mode.
	h.issueCommand(f.parent.Token, f.device.ID, "LOCK_NOW", nil)
	if !h.desiredState(f.parent.Token, f.device.ID, "").Desired.Locked {
		t.Fatal("LOCK_NOW is ignored in tracking-only mode")
	}
}

// ---- FR-2.3 / NFR-6: the phone can always be wiped from its own recovery menu ----
//
// The escape hatch. A fully managed device that sets no_factory_reset can be recovered from a bad
// policy, a wrong DNS host or a control plane that will not answer *only* through the control plane
// — and if the control plane is the thing that is broken, it cannot be recovered at all. So while
// this project is young, wiping from the recovery menu stays available, and it stays available in
// every state the policy can reach, not merely in the default one.
//
// The engine drops the four names from the restriction set after building it, and the unit suite
// covers that function. This test exists because the unit suite cannot see what the HTTP layer
// serialises or what the phone is actually handed: the DPC applies the payload from /device/policy,
// and that payload is the only thing that can lock a child's phone into an unwipeable state.
func TestFactoryResetIsNeverBlocked(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Game"},
			{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
		},
	}).expect(http.StatusOK)

	never := []string{"no_factory_reset", "no_outgoing_calls", "no_sms", "no_create_windows"}

	// Both views are checked, and neither is derived from the other here. The console's answer is
	// what a parent is shown; /device/policy is what the phone applies. A serialisation that dropped
	// the strip on one side only would be invisible to a test that asked once.
	hatchOpen := func(what, at string) {
		t.Helper()
		for _, view := range []struct {
			whose string
			state policyResponseDTO
		}{
			{"the console's answer", h.desiredState(f.parent.Token, f.device.ID, at)},
			{"the payload the phone applies", h.devicePolicy(f.deviceToken())},
		} {
			// A positive control on every single reading, not once for the whole test: an empty or
			// absent user_restrictions list would satisfy every mustNotHave below while proving
			// nothing at all, and it is exactly what a serialisation bug would produce.
			mustHave(t, view.state.Desired.UserRestrictions, "no_safe_boot",
				"hardening is in effect in "+view.whose+", "+what)
			for _, r := range never {
				mustNotHave(t, view.state.Desired.UserRestrictions, r,
					"in "+view.whose+", "+what+", the phone must still be wipeable from recovery (FR-2.3)")
			}
		}
	}

	hatchOpen("at enrolment", "")

	// Every state below is asserted to have actually been entered before the hatch is checked in it.
	// Sweeping states the server never reached would report four absences that mean nothing.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"allow_child_installs": false, "youtube_blocked": true, "dns_host": "kids.example-dns.test",
	})
	hardened := h.desiredState(f.parent.Token, f.device.ID, "")
	mustHave(t, hardened.Desired.UserRestrictions, "no_install_apps", "free installation was turned off")
	hatchOpen("with every control turned on", "")

	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{pkgGame: 60 * 60 * 1000},
	}).expect(http.StatusOK)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"daily_limit_minutes": 1, "bedtime_enabled": false,
	})
	if s := h.desiredState(f.parent.Token, f.device.ID, "").Desired; s.SuspendReason != "QUOTA" {
		t.Fatalf("the quota state was not reached; SuspendReason is %q", s.SuspendReason)
	}
	hatchOpen("with the daily quota spent", "")

	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": "21:00", "bedtime_end": "07:00",
		"timezone": "Europe/Zurich", "daily_limit_minutes": 0,
	})
	night := "2026-01-15T22:30:00%2B01:00"
	if s := h.desiredState(f.parent.Token, f.device.ID, night).Desired; s.SuspendReason != "BEDTIME" {
		t.Fatalf("the bedtime state was not reached; SuspendReason is %q", s.SuspendReason)
	}
	hatchOpen("inside the bedtime window", night)

	h.issueCommand(f.parent.Token, f.device.ID, "LOCK_NOW", nil)
	if !h.desiredState(f.parent.Token, f.device.ID, "").Desired.Locked {
		t.Fatal("the parent-lock state was not reached; the device does not read as locked")
	}
	hatchOpen("while a parent holds the device locked", "")

	// Tracking-only relaxes enforcement, so it is the state where a restriction is least expected —
	// and the one where an accidental default would be least likely to be noticed.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"tracking_only": true})
	if s := h.desiredState(f.parent.Token, f.device.ID, "").Desired; len(s.SuspendedPackages) != 0 {
		t.Fatalf("the tracking-only state was not reached; %d packages are still suspended", len(s.SuspendedPackages))
	}
	hatchOpen("in tracking-only mode", "")
}

// ---- FR-1.2/FR-1.3: the QR points at bytes this server will actually hand over ----

// A provisioning QR makes two claims about the DPC: where to download it, and what it hashes to.
// Both are checked elsewhere against what the server was configured with — which is a check that
// the server repeated its own configuration back. This one closes the loop instead: it downloads
// the URL out of the payload, over a real socket, with no credential of any kind, and hashes what
// comes back against the checksum the same payload published.
//
// That is the failure this cannot be allowed to have. A download location that points somewhere
// else — a release page, a bucket, a stale build — makes the checksum a claim about one artifact
// and the download a delivery of another. Nothing on this side goes red. The phone refuses the APK
// it has just fetched, halfway through setup, in front of a parent holding a wiped device, and the
// only explanation is in a system log they cannot read.
func TestTheQRPointsAtAnAPKThisServerServes(t *testing.T) {
	apkPath, apkSum := writeAPKFixture(t)
	h := newHarness(t, withSelfHostedAPK(apkPath))

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")
	prov, _ := h.provision(parent.Token, device.ID)

	location, ok := prov.Payload[extraDownloadLocation].(string)
	if !ok || location == "" {
		t.Fatalf("the payload carries no download location: %v", prov.Payload[extraDownloadLocation])
	}
	if want := h.base + apkDownloadPath; location != want {
		t.Fatalf("the QR sends the phone to %q; this server serves the DPC at %q", location, want)
	}
	published, ok := prov.Payload[extraPackageChecksum].(string)
	if !ok || published == "" {
		t.Fatalf("a server that hosts the DPC published no package checksum: %v", prov.Payload[extraPackageChecksum])
	}

	// No token, no cookie, and a client that shares nothing with the parent's: the setup wizard
	// fetches this on a factory-reset device that has no account and no way to acquire one. A route
	// that had quietly acquired an auth requirement would fail here and nowhere else.
	req, err := http.NewRequest(http.MethodGet, location, nil)
	if err != nil {
		t.Fatalf("build download request: %v", err)
	}
	resp, err := (&http.Client{Timeout: callTimeout}).Do(req)
	if err != nil {
		t.Fatalf("download the DPC from the URL in the QR: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the URL in the QR answers %d; a phone in the setup wizard cannot recover from that", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the download: %v", err)
	}

	sum := sha256.Sum256(body)
	if got := b64(sum[:]); got != published {
		t.Fatalf("the QR publishes checksum %s, the download hashes to %s — the phone would refuse it", published, got)
	}
	// And the same value once more against the bytes this test wrote, so a server that hashed the
	// response it was about to send would still be caught: agreeing with itself is not the claim.
	if published != apkSum {
		t.Fatalf("the QR publishes %s for an APK whose bytes hash to %s", published, apkSum)
	}
	if strings.ContainsAny(published, "=+/") {
		t.Fatalf("the package checksum encoding is not url-safe and unpadded: %q", published)
	}

	// Android hands the download to the package installer by content type. A server that let
	// net/http sniff the bytes reports an APK as a zip, and the distroless image has no
	// /etc/mime.types to correct it.
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.android.package-archive" {
		t.Fatalf("the DPC is served as %q", got)
	}
}

// ---- NFR-3: the stream is a wake-up, and it is addressed ------------------

func TestPushWakeUps(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// A second device for the same child, so the negative control has somewhere to be silent.
	other := h.newDevice(f.parent.Token, f.child.ID, "Spare phone")
	_, otherEnrollToken := h.provision(f.parent.Token, other.ID)
	otherEnrolled := h.enrollDevice(otherEnrollToken, "Pixel 6", "Android 13", nil)

	parentStream := h.openStream("/events", f.parent.Token)
	deviceStream := h.openStream("/device/stream", f.deviceToken())
	otherStream := h.openStream("/device/stream", otherEnrolled.DeviceToken)

	cmd := h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)

	if ev := deviceStream.next(10 * time.Second); ev.Type != "command" {
		t.Fatalf("the addressed device was woken with %q", ev.Type)
	}
	if ev := parentStream.next(10 * time.Second); ev.Type != "command" {
		t.Fatalf("the console was woken with %q", ev.Type)
	}
	// The negative control. A hub that published to everyone would satisfy both assertions above.
	otherStream.expectSilence(1500 * time.Millisecond)

	// …and the calibration for that silence: the same stream must still be able to receive. Without
	// this, a stream that died at connect time would pass expectSilence forever.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 90})
	if ev := otherStream.next(10 * time.Second); ev.Type != "policy" {
		t.Fatalf("the second device did not receive a policy change it should have: %q", ev.Type)
	}

	// NFR-3, stated as plainly as it can be tested: being woken is not being delivered.
	if state := h.commandState(f.parent.Token, f.device.ID, cmd.ID); state != "QUEUED" {
		t.Fatalf("the push event alone moved the command to %q; a phone in a tunnel would show as "+
			"having received an alarm it never got", state)
	}
	h.call(http.MethodGet, "/device/commands", f.deviceToken(), nil).expect(http.StatusOK)
	if state := h.commandState(f.parent.Token, f.device.ID, cmd.ID); state != "DELIVERED" {
		t.Fatalf("fetching did not mark the command delivered; it is %q", state)
	}
}

// ---- NFR-4: nothing lives only in memory ----------------------------------

func TestStateSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"daily_limit_minutes": 120, "timezone": "Europe/Zurich",
	})

	// Usage is filed under a calendar day, and a quota is only ever read for the day being asked
	// about — so this test has to ask about the day it wrote to. The other journeys pin a fixed
	// instant in January because they exercise bedtime windows, where the date is the point; here
	// that same fixed instant would read a day with no samples in it and report 0 minutes while the
	// row sat safely in the table. Both the report and the query are pinned to one day string, which
	// also removes the midnight race: a run that crosses midnight backfills yesterday (allowed for a
	// week) and reads yesterday, rather than writing one day and reading the next.
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatalf("Europe/Zurich: %v", err)
	}
	now := time.Now().In(zurich)
	day := now.Format("2006-01-02")
	at := url.QueryEscape(time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, zurich).Format(time.RFC3339))

	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"day":     day,
		"samples": map[string]int64{pkgGame: 30 * 60 * 1000},
	}).expect(http.StatusOK)
	queued := h.issueCommand(f.parent.Token, f.device.ID, "LOCATE_NOW", nil)
	before := h.desiredState(f.parent.Token, f.device.ID, at)
	if before.Desired.UsedMinutes != 30 {
		t.Fatalf("usage read back as %d minutes before any restart, so the restart assertion below "+
			"would prove nothing", before.Desired.UsedMinutes)
	}

	h.restart()

	// The device's credential is not a session: it must survive the process that issued it.
	var beat struct {
		PolicyVersion   int64 `json:"policy_version"`
		PendingCommands int   `json:"pending_commands"`
		Locked          bool  `json:"locked"`
	}
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(),
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK).decode(&beat)
	if beat.PendingCommands != 1 {
		t.Fatalf("a command queued before the restart is now %d pending; FR-9.2 says it waits",
			beat.PendingCommands)
	}

	// The parent's session outlives the restart too — the signing key is configuration, not a
	// per-process secret, which is what makes a rolling deployment invisible to a signed-in parent.
	after := h.desiredState(f.parent.Token, f.device.ID, at)
	if string(mustJSON(t, after.Desired)) != string(mustJSON(t, before.Desired)) {
		t.Fatalf("the desired state changed across a restart:\nbefore: %s\nafter:  %s",
			mustJSON(t, before.Desired), mustJSON(t, after.Desired))
	}
	if after.Desired.UsedMinutes != 30 {
		t.Fatalf("recorded usage came back as %d minutes", after.Desired.UsedMinutes)
	}
	if state := h.commandState(f.parent.Token, f.device.ID, queued.ID); state != "QUEUED" {
		t.Fatalf("the queued command is %q after the restart", state)
	}
}

// ---- FR-13: the console, and the mobile requirement it rests on -----------

func TestConsoleIsServedAndMobileReady(t *testing.T) {
	h := newHarness(t)

	index := h.call(http.MethodGet, "/", "", nil).expect(http.StatusOK)
	if ct := index.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("the console root is served as %q", ct)
	}
	body := string(index.Body)

	// FR-13.2 is measured from here. Without this meta tag the layout is composed against a 980 px
	// viewport and then scaled down: every media query is evaluated against a width the phone does
	// not have, so a mobile-first stylesheet renders as the desktop one and nothing reports it.
	if !strings.Contains(body, `name="viewport"`) || !strings.Contains(body, "width=device-width") {
		t.Fatal("the console has no width=device-width viewport meta, so the whole mobile layout is vacuous")
	}
	// FR-13.3: installable to the home screen.
	if !strings.Contains(body, `rel="manifest"`) {
		t.Fatal("the console does not reference a web app manifest (FR-13.3)")
	}

	// The console must be self-contained: a strict CSP forbids third-party origins, and a console
	// that silently fails to load a CDN is a console that shows nothing during an emergency.
	for _, external := range []string{"http://", "https://", "//cdn", "googleapis"} {
		if strings.Contains(body, external) {
			t.Fatalf("index.html references %q; the console is served under a self-only CSP", external)
		}
	}

	manifest := h.call(http.MethodGet, "/manifest.webmanifest", "", nil).expect(http.StatusOK)
	if ct := manifest.Header.Get("Content-Type"); ct != "application/manifest+json" {
		// Served as text/plain the install prompt never appears, and nothing anywhere reports it.
		t.Fatalf("the manifest is served as %q", ct)
	}
	var mf struct {
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	manifest.decode(&mf)
	if mf.Display != "standalone" || mf.StartURL == "" || len(mf.Icons) == 0 {
		t.Fatalf("the manifest would not install: %+v", mf)
	}

	// Every asset the console needs, with the security headers that make it safe to serve one.
	for _, path := range []string{"/index.html", "/app.css", "/app.js", "/icon.svg"} {
		resp := h.call(http.MethodGet, path, "", nil).expect(http.StatusOK)
		if resp.Header.Get("Content-Type") == "" {
			t.Fatalf("%s is served without a content type", path)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options is %q", path, got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Fatalf("%s: X-Frame-Options is %q", path, got)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("%s: the CSP is missing %q: %q", path, directive, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Fatalf("%s: the CSP allows %q", path, csp)
		}

		// A revalidating cache, proven by using it: the ETag must actually produce a 304, or every
		// parent re-downloads the console on every visit over a mobile connection (FR-13.3).
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("%s is served without an ETag", path)
		}
		req := h.newRequest(http.MethodGet, path, "", nil)
		req.Header.Set("If-None-Match", etag)
		if fresh := h.send(req); fresh.Status != http.StatusNotModified {
			t.Fatalf("%s: a matching If-None-Match got %d, not 304", path, fresh.Status)
		}
		// The negative half: a wrong validator must still return the body, or the 304 above would be
		// unconditional and the console could never be updated.
		req = h.newRequest(http.MethodGet, path, "", nil)
		req.Header.Set("If-None-Match", `"not-the-current-etag"`)
		if stale := h.send(req); stale.Status != http.StatusOK || len(stale.Body) == 0 {
			t.Fatalf("%s: a stale If-None-Match got %d with %d bytes", path, stale.Status, len(stale.Body))
		}
	}

	// The health endpoints an orchestrator polls. /healthz answers as long as the process is up;
	// /readyz speaks for the database, which is what makes a rolling deploy wait.
	h.call(http.MethodGet, "/healthz", "", nil).expect(http.StatusOK)
	h.call(http.MethodGet, "/readyz", "", nil).expect(http.StatusOK)
}

// ---- FR-13.1: both ways in ------------------------------------------------

func TestBrowserSignInJourney(t *testing.T) {
	h := newHarness(t)

	result := h.browserSignIn(primaryParent)
	token := result.Get("token")
	if token == "" {
		t.Fatalf("the browser flow ended with %v and no token", result)
	}
	if result.Get("error") != "" {
		t.Fatalf("the browser flow reported %q", result.Get("error"))
	}
	if exp := result.Get("expires"); exp == "" {
		t.Fatal("the console is given a token with no expiry, so it cannot know when to sign in again")
	}

	// The token from the browser flow is a session like any other.
	var me parentDTO
	h.call(http.MethodGet, "/me", token, nil).expect(http.StatusOK).decode(&me)
	if me.Email != primaryParent.Email {
		t.Fatalf("the browser session belongs to %q", me.Email)
	}
	if me.Role != "PRIMARY_ADMIN" {
		t.Fatalf("the browser session has role %q", me.Role)
	}

	// The session must not ride in the query string: a fragment never reaches a proxy log, an access
	// log or a Referer header, and a query parameter reaches all three.
	if strings.Contains(token, "?") {
		t.Fatal("the session token was returned in the query string")
	}
	if h.logs.contains(token) {
		t.Fatal("the server logged the session token it issued")
	}
}
