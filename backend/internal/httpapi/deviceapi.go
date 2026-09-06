package httpapi

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/auth"
	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/store"
)

// usageBackfillDays bounds how far into the past a device may file screen time. A phone that was
// off for a week has real usage to report for the days it was on; a phone with a wrong clock has
// not. Seven days admits the first and refuses the second.
const usageBackfillDays = 7

type enrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	Model           string `json:"model"`
	OSVersion       string `json:"os_version"`
	// CriticalPackages is what this hardware calls its dialer, launcher and IME. The server unions
	// it with the built-in list and never narrows it, so a device that reports nothing is no worse
	// off than the floor, and a device that reports its OEM dialer cannot have it suspended.
	CriticalPackages []string `json:"critical_packages"`
}

type enrollResponse struct {
	DeviceToken string `json:"device_token"`
	DeviceID    string `json:"device_id"`
	ChildID     string `json:"child_id"`
	// Recovery is the material for verifying the parent's recovery code with no network (FR-12.3).
	// The plaintext code is not here: the device must be able to check a code, not to display one.
	Recovery recoveryMaterial `json:"recovery"`
}

type recoveryMaterial struct {
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Hash       string `json:"hash"`
}

// enroll exchanges a single-use enrollment token for a device credential (FR-1.4).
//
// A replayed token and a token that never existed are answered identically, and that is by
// construction rather than by choice: single use is enforced by clearing the hash inside the same
// UPDATE that matches it, so a second attempt matches zero rows and there is nothing left to tell
// the two apart. Both are 409 — the request is well-formed, the state does not permit it.
func (s *Server) enroll(c *gin.Context) {
	var req enrollRequest
	if !bindJSON(c, &req) {
		return
	}
	token := strings.TrimSpace(req.EnrollmentToken)
	if token == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "enrollment_token is required")
		return
	}

	deviceToken, deviceHash, err := auth.NewToken()
	if err != nil {
		s.fail(c, err)
		return
	}
	code, salt, iterations, hash, err := auth.NewRecoveryCode()
	if err != nil {
		s.fail(c, err)
		return
	}

	critical, droppedCritical := sanitizeCriticalPackages(req.CriticalPackages)

	result, err := s.store.ConsumeEnrollment(c.Request.Context(), auth.HashToken(token), deviceHash,
		store.RecoverySecret{Code: code, Salt: salt, Iterations: iterations, Hash: hash},
		strings.TrimSpace(req.Model), strings.TrimSpace(req.OSVersion), critical)
	if err != nil {
		s.log.Warn("enrollment refused", "error", err, "client", c.ClientIP(),
			"request_id", RequestIDOf(c))
		failWith(c, http.StatusConflict, "conflict",
			"this enrollment token is not valid, has expired, or has already been used")
		return
	}

	s.audit(c, store.ActorDevice, result.Device.ID.String(), "DEVICE_ENROLLED", "device",
		result.Device.ID.String(), map[string]any{
			"model": result.Device.Model, "os_version": result.Device.OSVersion,
			// The list itself, not its length. FR-5.5 makes this the one input the managed party
			// supplies to its own policy, so a parent has to be able to read back what this phone
			// claimed rather than only how many claims it made.
			"critical_packages": result.Device.CriticalPackages,
			// Recorded even when zero: it is the difference between a phone that reported eight
			// packages and one that tried to report four hundred.
			"critical_packages_dropped": droppedCritical,
		})
	s.hub.PublishParents(Event{Type: "device", DeviceID: result.Device.ID.String(),
		ChildID: result.ChildID.String()})

	c.JSON(http.StatusOK, enrollResponse{
		DeviceToken: deviceToken,
		DeviceID:    result.Device.ID.String(),
		ChildID:     result.ChildID.String(),
		Recovery: recoveryMaterial{
			Salt:       base64.RawURLEncoding.EncodeToString(salt),
			Iterations: iterations,
			Hash:       base64.RawURLEncoding.EncodeToString(hash),
		},
	})
}

type heartbeatRequest struct {
	BatteryLevel  *int   `json:"battery_level"`
	Charging      *bool  `json:"charging"`
	ScreenOn      *bool  `json:"screen_on"`
	Connectivity  string `json:"connectivity"`
	PolicyVersion int64  `json:"policy_version"`

	// The DPC build running on the phone. Absent from an older device's heartbeat, and absence
	// leaves the stored value alone rather than clearing it — the alternative would make every
	// heartbeat from a phone that has not been updated erase the version of one that has.
	AppVersionName string `json:"app_version_name"`
	AppVersionCode int64  `json:"app_version_code"`

	// UsageAccess is whether the phone may read usage stats (FR-3.6). A pointer, and absence is
	// carried through as absence: an older DPC omits it, and recording that as "no access" would
	// put a false warning on every phone that has not been updated.
	UsageAccess *bool `json:"usage_access"`

	// UpdateError is why the last self-update did not end with a new build running, in the
	// platform's own words, or "" when the phone has nothing to report (FR-15.7). A pointer for
	// the same reason as the field above and with a sharper edge: this is the field that says an
	// update FAILED, and letting an older DPC's heartbeat clear it would hide exactly the failure
	// it exists to surface.
	UpdateError *string `json:"update_error"`
}

// maxUpdateErrorRunes bounds what one phone may write into the field a parent reads.
//
// The text is the platform's own message and is displayed verbatim, which is the point of it — a
// paraphrase would be this server guessing at an Android error it has never seen. Bounding it is
// not distrust of the DPC so much as of the platform: `EXTRA_STATUS_MESSAGE` has no documented
// limit, and a console line is a console line.
const maxUpdateErrorRunes = 400

// clampUpdateError normalises what the phone sent, preserving the three states the column needs.
//
// nil stays nil — a DPC that does not report the field must not clear what a newer one wrote.
// Everything else is trimmed and truncated; a value that is only whitespace becomes "", which is
// the phone saying it has nothing to report and is what clears the line.
func clampUpdateError(reported *string) *string {
	if reported == nil {
		return nil
	}
	text := strings.TrimSpace(*reported)
	if r := []rune(text); len(r) > maxUpdateErrorRunes {
		text = string(r[:maxUpdateErrorRunes])
	}
	return &text
}

// heartbeat records liveness and tells the device whether it is behind.
//
// The response carries the current policy version and the number of commands waiting, so a device
// whose stream was cut still converges on its own schedule. The stream makes that fast; the
// heartbeat is what makes it certain.
func (s *Server) heartbeat(c *gin.Context) {
	dev := deviceOf(c)
	var req heartbeatRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.BatteryLevel != nil && (*req.BatteryLevel < 0 || *req.BatteryLevel > 100) {
		failWith(c, http.StatusBadRequest, "invalid_input", "battery_level must be 0..100")
		return
	}

	if err := s.store.TouchDevice(c.Request.Context(), dev.ID, store.DeviceState{
		BatteryLevel:  req.BatteryLevel,
		Charging:      req.Charging,
		ScreenOn:      req.ScreenOn,
		Connectivity:  req.Connectivity,
		PolicyVersion: req.PolicyVersion,

		AppVersionName: strings.TrimSpace(req.AppVersionName),
		AppVersionCode: req.AppVersionCode,
		UsageAccess:    req.UsageAccess,

		ReportedUpdateError: clampUpdateError(req.UpdateError),
	}); err != nil {
		s.fail(c, err)
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	pending, err := s.store.PendingCommands(c.Request.Context(), dev.ID)
	if err != nil {
		s.fail(c, err)
		return
	}

	s.hub.PublishParents(Event{Type: "state", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, gin.H{
		"policy_version":   pol.Version,
		"pending_commands": len(pending),
		"locked":           dev.Locked,
		"server_time":      s.now().Format(time.RFC3339),
	})
}

// devicePolicy answers with the desired state and with the input it was computed from.
//
// Both, deliberately. The desired state is what to apply right now; the input is what lets the
// device recompute the same answer offline, at 21:00, with no network — using the same engine and
// the same shared vectors. Sending only the output would make every enforcement decision depend on
// connectivity, which is the one thing a child's phone can always remove.
func (s *Server) devicePolicy(c *gin.Context) {
	dev := deviceOf(c)
	state, input, err := s.resolver.Resolve(c.Request.Context(), dev.ID, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"desired": state, "input": input})
}

// deviceCommands hands over the queued commands and records that it did.
//
// Delivery is recorded here, on a fetch that actually returned them, and nowhere else. The push
// event says only "there is something to fetch": if it marked a command delivered, a phone in a
// tunnel would show as having received an alarm it never got (NFR-3).
func (s *Server) deviceCommands(c *gin.Context) {
	dev := deviceOf(c)
	cmds, err := s.store.PendingCommands(c.Request.Context(), dev.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	// The rows were read before they were marked, so the copies handed to the device still say
	// QUEUED. Carry the recorded state onto them rather than answering with a payload that
	// contradicts the row the server just wrote — a device that stores what it was given would
	// otherwise disagree with the console about the same command forever.
	for i, cmd := range cmds {
		at, err := s.store.MarkDelivered(c.Request.Context(), cmd.ID)
		if err != nil {
			s.fail(c, err)
			return
		}
		cmds[i].State = store.CmdDelivered
		cmds[i].DeliveredAt = &at
	}
	if len(cmds) > 0 {
		s.hub.PublishParents(Event{Type: "command", DeviceID: dev.ID.String(),
			ChildID: dev.ChildID.String()})
	}
	c.JSON(http.StatusOK, gin.H{"commands": cmds})
}

type inventoryRequest struct {
	Apps []inventoryApp `json:"apps"`
}

type inventoryApp struct {
	PackageName string `json:"package_name"`
	Label       string `json:"label"`
	SystemApp   bool   `json:"system_app"`
	// What the device says it is enforcing right now (FR-18.6). Absent from an older DPC's report,
	// which decodes as false — "nothing known to be restrained", which is the honest reading of a
	// phone that does not send the field.
	Hidden    bool `json:"hidden"`
	Suspended bool `json:"suspended"`
}

func (s *Server) deviceInventory(c *gin.Context) {
	dev := deviceOf(c)
	var req inventoryRequest
	if !bindJSON(c, &req) {
		return
	}
	apps := make([]store.InstalledApp, 0, len(req.Apps))
	for _, a := range req.Apps {
		if strings.TrimSpace(a.PackageName) == "" {
			continue
		}
		apps = append(apps, store.InstalledApp{
			PackageName: a.PackageName, Label: a.Label, SystemApp: a.SystemApp,
			Hidden: a.Hidden, Suspended: a.Suspended,
		})
	}
	if err := s.store.ReplaceInstalledApps(c.Request.Context(), dev.ID, apps); err != nil {
		s.fail(c, err)
		return
	}
	s.hub.PublishParents(Event{Type: "inventory", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, gin.H{"apps": len(apps)})
}

type usageRequest struct {
	// Day is optional. Omitted means "today in the child's timezone", resolved by the server —
	// which is the only party that knows the policy's zone and whose clock a child cannot change.
	Day     string           `json:"day"`
	Samples map[string]int64 `json:"samples"`
}

func (s *Server) deviceUsageReport(c *gin.Context) {
	dev := deviceOf(c)
	var req usageRequest
	if !bindJSON(c, &req) {
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	today, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}

	day := strings.TrimSpace(req.Day)
	switch {
	case day == "":
		day = today
	case !validDay(day):
		failWith(c, http.StatusBadRequest, "invalid_input", "day must look like 2026-08-17")
		return
	case day > today:
		// A future day would let a device with a wrong or tampered clock park usage where today's
		// quota can never see it, which is a way to earn unlimited screen time (FR-3.2).
		failWith(c, http.StatusBadRequest, "invalid_input", "day is in the future")
		return
	case day < earliestBackfill(today):
		failWith(c, http.StatusBadRequest, "invalid_input", "day is more than a week old")
		return
	}

	if err := s.store.RecordUsage(c.Request.Context(), dev.ID, day, req.Samples); err != nil {
		s.fail(c, err)
		return
	}
	minutes, err := s.store.UsageMinutesForDay(c.Request.Context(), dev.ID, day)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.hub.PublishParents(Event{Type: "usage", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, gin.H{"day": day, "minutes": minutes})
}

// earliestBackfill is today minus the backfill window, in the same calendar the day key uses.
func earliestBackfill(today string) string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return today
	}
	return t.AddDate(0, 0, -usageBackfillDays).Format("2006-01-02")
}

type locationRequest struct {
	Latitude   float64  `json:"latitude"`
	Longitude  float64  `json:"longitude"`
	AccuracyM  *float64 `json:"accuracy_m"`
	CapturedAt string   `json:"captured_at"`
}

func (s *Server) deviceLocationReport(c *gin.Context) {
	dev := deviceOf(c)
	var req locationRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Latitude < -90 || req.Latitude > 90 || req.Longitude < -180 || req.Longitude > 180 {
		failWith(c, http.StatusBadRequest, "invalid_input", "latitude/longitude out of range")
		return
	}
	captured := s.now()
	if req.CapturedAt != "" {
		t, err := time.Parse(time.RFC3339, req.CapturedAt)
		if err != nil {
			failWith(c, http.StatusBadRequest, "invalid_input", "captured_at must be RFC 3339")
			return
		}
		captured = t
	}
	loc, err := s.store.AddLocation(c.Request.Context(), dev.ID, req.Latitude, req.Longitude,
		req.AccuracyM, captured)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.hub.PublishParents(Event{Type: "location", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, loc)
}

type ackRequest struct {
	OK     bool           `json:"ok"`
	Result map[string]any `json:"result"`
	Error  string         `json:"error"`
}

// ackCommand records the device's own report of what happened.
//
// The device id comes from the authenticated token and is part of the WHERE clause, so one device
// cannot acknowledge another's command — including one it learned the id of from a shared network.
func (s *Server) ackCommand(c *gin.Context) {
	dev := deviceOf(c)
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req ackRequest
	if !bindJSON(c, &req) {
		return
	}
	cmd, err := s.store.AckCommand(c.Request.Context(), dev.ID, id, req.OK, req.Result, req.Error)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.audit(c, store.ActorDevice, dev.ID.String(), "COMMAND_"+cmd.State, "device", dev.ID.String(),
		map[string]any{"type": cmd.Type, "command": cmd.ID.String(), "error": req.Error})
	s.hub.PublishParents(Event{Type: "command", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, cmd)
}

type recoveryEventRequest struct {
	Succeeded  bool   `json:"succeeded"`
	OccurredAt string `json:"occurred_at"`
}

// recoveryEventReport records that someone used the offline recovery code (FR-12.4).
//
// Failures are recorded as well as successes, and both reach the console: a run of failed attempts
// is what a parent needs to see, and it is the only signal that distinguishes a child guessing from
// a parent who mistyped once.
func (s *Server) recoveryEventReport(c *gin.Context) {
	dev := deviceOf(c)
	var req recoveryEventRequest
	if !bindJSON(c, &req) {
		return
	}
	occurred := s.now()
	if req.OccurredAt != "" {
		t, err := time.Parse(time.RFC3339, req.OccurredAt)
		if err != nil {
			failWith(c, http.StatusBadRequest, "invalid_input", "occurred_at must be RFC 3339")
			return
		}
		occurred = t
	}
	if err := s.store.RecordRecoveryEvent(c.Request.Context(), dev.ID, req.Succeeded, occurred); err != nil {
		s.fail(c, err)
		return
	}
	s.audit(c, store.ActorDevice, dev.ID.String(), "RECOVERY_CODE_USED", "device", dev.ID.String(),
		map[string]any{"succeeded": req.Succeeded, "occurred_at": occurred.Format(time.RFC3339)})
	s.hub.PublishParents(Event{Type: "recovery", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, gin.H{"recorded": true})
}

// apkInfo tells an enrolled device what the DPC it should be running is.
//
// **This is the metadata half of UPDATE_APP** (FR-15.1), and it exists as its own endpoint rather
// than as command parameters for one reason: a command carries what the parent asked for, and this
// carries what is true right now. The APK on the node can be replaced between the moment a parent
// presses the button and the moment the phone in a school bag comes back online, and a checksum
// baked into the queued row would then describe the previous build — which is the exact drift the
// download handler refuses to serve.
//
// The checksum is the same value every provisioning QR carries, computed at startup from the file
// on disk. Handing it out here is not a disclosure: the endpoint below it serves the very bytes it
// hashes, unauthenticated, by design.
//
// Authenticated as the device, unlike /dpc.apk, because there is a device to authenticate. The
// download must be reachable by a factory-reset phone that has no credential yet; a phone asking
// whether it should update has been enrolled for weeks.
func (s *Server) apkInfo(c *gin.Context) {
	if s.cfg.APKPath == "" || s.packageChecksum == "" {
		// Not an error state. A control plane may legitimately not host the DPC — and a device that
		// is told so stops asking, rather than downloading something this server never described.
		failWith(c, http.StatusNotFound, "not_found", "this server does not host the DPC")
		return
	}
	info, err := os.Stat(s.cfg.APKPath)
	if err != nil || info.IsDir() {
		s.log.Error("the configured APK is no longer readable",
			"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
		return
	}
	out := gin.H{
		// Absolute, from the same PublicURL the provisioning QR is built from, so the device never
		// has to join a base to a path and never has to guess a scheme.
		"url":              strings.TrimSuffix(s.cfg.PublicURL.String(), "/") + APKDownloadPath,
		"package_checksum": s.packageChecksum,
		// The size is advisory and is sent anyway: it is what lets the device refuse a download
		// that ended early before spending the CPU to hash 13 MB, and what makes "the proxy
		// truncated it" distinguishable from "the file changed" in the failure the parent sees.
		"size": info.Size(),
	}
	// **What makes an automatic update possible at all** (FR-15.6). Without a version here the only
	// way for a phone to learn whether it is behind is to download 13 MB and read the archive, which
	// is affordable once when a parent presses a button and not on a timer. It is read from the APK
	// on disk at startup, by the same parser the app catalog uses — never configured, never typed.
	//
	// Absent when the file could not be parsed, and absent is not zero: the device treats a missing
	// version as "the server did not say" and falls back to downloading, which is the behaviour it
	// had before this field existed.
	if s.hostedAPK != nil {
		out["package_name"] = s.hostedAPK.PackageName
		out["version_code"] = s.hostedAPK.VersionCode
		out["version_name"] = s.hostedAPK.VersionName
	}
	c.JSON(http.StatusOK, out)
}

// maxCriticalPackages bounds what one device may add to the critical whitelist.
//
// FR-5.5 exempts a CATEGORY — the dialer, messaging, contacts, emergency information, settings and
// the package installer — and only the phone knows which package on this hardware is each of those,
// so the list has to come from the device. That makes it the one input to the enforcement engine
// supplied by the party the policy is applied to, and an entry in it is exempt from bedtime, from
// an exhausted quota, and from a parent's explicit block rule (see policy.Compute).
//
// Bounding it does not make the claim trustworthy — nothing here can tell a real OEM dialer from a
// game — and it is not pretending to. It bounds the damage of a wrong or hostile one to something a
// person can read: a real phone resolves well under ten, `CriticalPackages.kt` produces at most a
// handful, and the audit entry now carries the names. Six of eight is a phone; two hundred is a
// claim somebody made up, and before this cap the row simply grew.
const maxCriticalPackages = 32

// maxPackageNameLength is Android's own limit on a package name (PackageParser).
const maxPackageNameLength = 255

// sanitizeCriticalPackages keeps the entries that could be package names, up to the cap, and
// reports how many it dropped.
//
// It never fails the enrollment. A phone in the middle of provisioning, in front of a parent
// holding a wiped device, must not be turned away over a malformed entry in an advisory list — the
// engine's built-in whitelist is the floor and it is unaffected, so the worst case of dropping
// everything is a device with the default exemptions.
func sanitizeCriticalPackages(in []string) (kept []string, dropped int) {
	kept = []string{}
	seen := map[string]struct{}{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" {
			// Not counted as dropped: an empty string carries no claim, and the engine already
			// ignores it. Counting it would make a trailing comma look like an attack.
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		if !validPackageName(p) || len(kept) >= maxCriticalPackages {
			dropped++
			continue
		}
		seen[p] = struct{}{}
		kept = append(kept, p)
	}
	return kept, dropped
}

// validPackageName reports whether s has the shape Android requires of a package name: two or more
// dot-separated segments, each starting with a letter or underscore and continuing with letters,
// digits or underscores.
//
// Shape only. A name of the right shape is not a real package and is not a dialer; this rejects
// the values that could never be either — a URL, a path, a sentence, a 4 KB blob.
func validPackageName(s string) bool {
	if len(s) == 0 || len(s) > maxPackageNameLength {
		return false
	}
	segments := strings.Split(s, ".")
	if len(segments) < 2 {
		return false
	}
	for _, seg := range segments {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
			case c >= '0' && c <= '9':
				if i == 0 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
