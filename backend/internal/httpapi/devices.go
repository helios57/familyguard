package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/auth"
	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/provisioning"
	"github.com/helios57/familyguard/backend/internal/store"
)

// enrollmentWindow is how long a freshly minted QR stays valid. Long enough to walk to the child's
// phone and finish the setup wizard, short enough that a screenshot left in a chat is not a
// standing invitation.
const enrollmentWindow = 30 * time.Minute

type createDeviceRequest struct {
	Name string `json:"name"`
}

func (s *Server) createDevice(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req createDeviceRequest
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "a device needs a name")
		return
	}

	// The device is created without an enrollment credential. Minting one is the provisioning
	// endpoint's job and happens when the parent is actually standing in front of the phone, which
	// is what keeps the 30-minute window meaningful rather than expiring while nobody is looking.
	dev, err := s.store.CreateDevice(c.Request.Context(), childID, name)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "DEVICE_ADDED", "device", dev.ID.String(), map[string]any{
		"name": dev.Name, "child": childID.String(),
	})
	s.hub.PublishParents(Event{Type: "device", DeviceID: dev.ID.String(), ChildID: childID.String()})
	c.JSON(http.StatusCreated, dev)
}

func (s *Server) listDevices(c *gin.Context) {
	var childID *uuid.UUID
	if raw := c.Query("child_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			failWith(c, http.StatusBadRequest, "invalid_id", "child_id is not a valid id")
			return
		}
		childID = &id
	}
	devices, err := s.store.ListDevices(c.Request.Context(), childID, s.cfg.DeviceOfflineAfter)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"devices": devices})
}

func (s *Server) getDevice(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	dev, err := s.store.GetDevice(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	// A device that has never checked in has no state row. That is reported as an absent state
	// rather than as a zeroed one: "battery 0%, offline" is a measurement, and we have not taken it.
	state, err := s.store.GetDeviceState(c.Request.Context(), id, s.cfg.DeviceOfflineAfter)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"device": dev, "state": state, "enrolled": dev.EnrolledAt != nil})
}

func (s *Server) renameDevice(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req createDeviceRequest
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "a device needs a name")
		return
	}
	if err := s.store.RenameDevice(c.Request.Context(), id, name); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "DEVICE_RENAMED", "device", id.String(), map[string]any{"name": name})
	c.JSON(http.StatusOK, gin.H{"id": id, "name": name})
}

func (s *Server) deleteDevice(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.DeleteDevice(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "DEVICE_REMOVED", "device", id.String(), nil)
	s.hub.PublishParents(Event{Type: "device", DeviceID: id.String()})
	c.Status(http.StatusNoContent)
}

// provisioningRequest is the body of the provisioning POST. It is optional, and its one field is
// an acknowledgement rather than an option: see mintProvisioning.
type provisioningRequest struct {
	ReplaceEnrolled bool `json:"replace_enrolled"`
}

// provisioningPayload mints a fresh enrollment token and returns the QR that carries it.
//
// It is a POST, and it mints rather than reads, for one reason: the plaintext enrollment token is
// never stored. Only its hash is, so there is no "show me the existing QR" that could work — and
// making the endpoint mint means the QR a parent is looking at is always one that will succeed.
// The previous token stops working at the same moment, which is the single-use property held one
// step earlier than enrollment.
func (s *Server) provisioningPayload(c *gin.Context) {
	params, dev, ok := s.mintProvisioning(c)
	if !ok {
		return
	}
	payload, err := provisioning.Payload(params)
	if err != nil {
		s.fail(c, err)
		return
	}
	raw, err := provisioning.PayloadJSON(params)
	if err != nil {
		s.fail(c, err)
		return
	}
	svg, err := provisioning.QRSVG(raw, 6, 4)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"device":  dev,
		"payload": payload,
		"svg":     string(svg),
		// The same token the QR carries, spelled out so it can be read aloud and typed (FR-1.8).
		// A phone that is already a device owner cannot be provisioned again — there is no welcome
		// screen to tap six times without a factory reset — so the QR is unreachable on exactly the
		// device that needs a new credential most. The console shows this next to the QR, and it is
		// what a parent types into the phone's "Re-link this phone" field.
		//
		// It is not a second secret: it is the identical single-use token, and it is already inside
		// `payload`. Surfacing it as its own field only means the console does not have to know the
		// shape of the admin extras bundle to find it.
		"setup_code": params.EnrollmentToken,
		"expires_at": s.now().Add(enrollmentWindow),
	})
}

// mintProvisioning issues the enrollment token and assembles the parameters. Split out because the
// SVG-only endpoint needs exactly the same work and must not diverge from it.
func (s *Server) mintProvisioning(c *gin.Context) (provisioning.Params, *store.Device, bool) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return provisioning.Params{}, nil, false
	}
	dev, err := s.store.GetDevice(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return provisioning.Params{}, nil, false
	}
	var req provisioningRequest
	if !bindOptionalJSON(c, &req) {
		return provisioning.Params{}, nil, false
	}
	// Minting is DESTRUCTIVE on an enrolled device, and nothing about the request says so.
	//
	// ResetEnrollment clears device_token_hash, so the phone that is currently enrolled is revoked
	// the instant a new QR exists. Measured on the first real phone on 2026-09-06, where "Setup QR"
	// on a working device read as "show me that code again" and disconnected it (ENROLLMENT_ISSUED
	// 02:01:18, an hour after DEVICE_ENROLLED 00:55:27). At the time there was no way back at all:
	// the DPC never re-enrolled while it still held a credential, so the phone was a factory reset.
	// FR-1.8 gives it one, but the cost is still that somebody has to physically pick the phone up
	// and type a code into it, and a phone running an older build still cannot do even that.
	//
	// So the acknowledgement is required rather than the consequence being documented. A caller
	// that has not said it means to replace the phone gets 409 and its credential intact; the cost
	// of being wrong in this direction is a second click, and in the other direction it is a wipe.
	if dev.EnrolledAt != nil && !req.ReplaceEnrolled {
		failWith(c, http.StatusConflict, "already_enrolled",
			"this device is already enrolled, and issuing a new setup code revokes it: the phone "+
				"stops reporting at once, and getting it back means picking it up and typing the "+
				"new code into it. Send "+
				`{"replace_enrolled": true} if that is what you mean to do.`)
		return provisioning.Params{}, nil, false
	}
	if s.cfg.APKURL == nil {
		// Refusing here beats emitting a QR without a download location: that QR provisions the
		// device into a state with no DPC and no way to reach this server.
		failWith(c, http.StatusInternalServerError, "misconfigured",
			"APK_URL is not configured, so no device can download the app")
		return provisioning.Params{}, nil, false
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		s.fail(c, err)
		return provisioning.Params{}, nil, false
	}
	if err := s.store.ResetEnrollment(c.Request.Context(), id, hash, s.now().Add(enrollmentWindow)); err != nil {
		s.fail(c, err)
		return provisioning.Params{}, nil, false
	}

	s.auditParent(c, "ENROLLMENT_ISSUED", "device", id.String(), map[string]any{
		"expires_at": s.now().Add(enrollmentWindow).Format(time.RFC3339),
		// Recorded because the two cases are a first setup and a revocation, and the audit trail is
		// where a second parent finds out which one happened.
		"replaced_enrolled": dev.EnrolledAt != nil,
	})
	return provisioning.Params{
		Component:         s.cfg.DPCComponent,
		APKURL:            s.cfg.APKURL.String(),
		SignatureChecksum: s.signatureChecksum,
		PackageChecksum:   s.packageChecksum,
		ServerURL:         strings.TrimSuffix(s.cfg.PublicURL.String(), "/"),
		EnrollmentToken:   token,
		DeviceID:          id.String(),
		LeaveSystemAppsOn: true,
	}, dev, true
}

// recoveryCode returns the device's offline recovery code (FR-12.3).
//
// Reading it is audited, deliberately. The code is what unlocks a device with no network, so who
// looked at it and when is exactly the kind of thing a second parent needs to be able to see.
func (s *Server) recoveryCode(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	code, err := s.store.RecoveryCode(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failWith(c, http.StatusNotFound, "not_found",
				"this device has no recovery code; it is generated when the device enrolls")
			return
		}
		s.fail(c, err)
		return
	}
	s.auditParent(c, "RECOVERY_CODE_VIEWED", "device", id.String(), nil)
	c.JSON(http.StatusOK, gin.H{"device_id": id, "recovery_code": code})
}

func (s *Server) listRecoveryEvents(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	events, err := s.store.ListRecoveryEvents(c.Request.Context(), id, queryInt(c, "limit", 50, 1, 500))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

func (s *Server) listDeviceApps(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	apps, err := s.store.ListInstalledApps(c.Request.Context(), id, c.Query("include_system") == "1")
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"apps": apps})
}

// deviceUsage reports screen time for the child's local day, not the server's.
func (s *Server) deviceUsage(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	dev, err := s.store.GetDevice(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	day := strings.TrimSpace(c.Query("day"))
	if day == "" {
		if day, err = enforce.DayKey(pol, s.now()); err != nil {
			s.fail(c, err)
			return
		}
	} else if !validDay(day) {
		failWith(c, http.StatusBadRequest, "invalid_input", "day must look like 2026-08-17")
		return
	}

	samples, err := s.store.UsageForDay(c.Request.Context(), id, day)
	if err != nil {
		s.fail(c, err)
		return
	}
	history, err := s.store.UsageHistory(c.Request.Context(), id, queryInt(c, "days", 14, 1, 90))
	if err != nil {
		s.fail(c, err)
		return
	}
	minutes, err := s.store.UsageMinutesForDay(c.Request.Context(), id, day)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"day": day, "timezone": pol.Timezone, "minutes": minutes,
		"packages": samples, "history": history,
	})
}

func (s *Server) deviceLocations(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	locs, err := s.store.ListLocations(c.Request.Context(), id, queryInt(c, "limit", 50, 1, 500))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"locations": locs})
}

// deviceDesiredState shows the parent exactly what the device is being told to do, computed by the
// same function that answers the device. Not a description of it, not a re-implementation for
// display: the identical call. A console that showed its own idea of the state would be a second
// source of truth, and the first symptom of the two disagreeing would be a parent insisting the
// setting is on while the phone behaves as though it is off.
func (s *Server) deviceDesiredState(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	at := s.now()
	if raw := c.Query("at"); raw != "" {
		// "What will bedtime do at 21:05?" is a question a parent should be able to ask without
		// waiting until 21:05.
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			failWith(c, http.StatusBadRequest, "invalid_input", "at must be an RFC 3339 timestamp")
			return
		}
		at = parsed
	}
	state, input, err := s.resolver.Resolve(c.Request.Context(), id, at)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"desired": state, "input": input})
}

func (s *Server) listCommands(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	cmds, err := s.store.ListCommands(c.Request.Context(), id, queryInt(c, "limit", 50, 1, 200))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"commands": cmds})
}

type createCommandRequest struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// createCommand queues an instant action (FR-9).
//
// Two of these change durable state as well as sending a command, and both do it here rather than
// waiting for the device to confirm:
//
//   - LOCK_NOW and UNLOCK_DEVICE set devices.locked. A lock held only as an unexpired command would
//     be undone by a reboot or by the command's own TTL, which is the opposite of what a parent who
//     locked a phone expects.
//   - BLOCK_YOUTUBE_ALL and UNBLOCK_YOUTUBE_ALL write the policy. The command is the fast path; the
//     policy is what survives a reinstall, and what a device that was offline picks up on its next
//     sync.
//
// The command row itself stays QUEUED. Nothing here marks it delivered — only a device actually
// fetching it does that (NFR-3).
func (s *Server) createCommand(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req createCommandRequest
	if !bindJSON(c, &req) {
		return
	}
	if !store.ValidCommandTypes[req.Type] {
		failWith(c, http.StatusBadRequest, "invalid_input", "unknown command type "+req.Type)
		return
	}
	dev, err := s.store.GetDevice(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	if dev.EnrolledAt == nil {
		// Queueing for a device that has never enrolled produces a row nothing will ever fetch, and
		// a console that shows "sent". Saying so is the honest answer.
		failWith(c, http.StatusConflict, "conflict", "this device has not enrolled yet")
		return
	}

	switch req.Type {
	case store.CmdTypeLockNow:
		if err := s.store.SetLocked(c.Request.Context(), id, true); err != nil {
			s.fail(c, err)
			return
		}
	case store.CmdTypeUnlockDevice:
		if err := s.store.SetLocked(c.Request.Context(), id, false); err != nil {
			s.fail(c, err)
			return
		}
	case store.CmdTypeBlockYouTube, store.CmdTypeUnblockYouTube:
		blocked := req.Type == store.CmdTypeBlockYouTube
		if _, err := s.store.UpdatePolicy(c.Request.Context(), dev.ChildID,
			store.PolicyUpdate{YouTubeBlocked: &blocked}); err != nil {
			s.fail(c, err)
			return
		}
	}

	cmd, err := s.store.QueueCommand(c.Request.Context(), id, req.Type, req.Params,
		parentID(c), s.cfg.CommandTTL)
	if err != nil {
		s.fail(c, err)
		return
	}

	s.auditParent(c, "COMMAND_ISSUED", "device", id.String(), map[string]any{
		"type": req.Type, "command": cmd.ID.String(),
	})
	s.hub.PublishDevice(id, Event{Type: "command", ChildID: dev.ChildID.String()})
	s.hub.PublishParents(Event{Type: "command", DeviceID: id.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusAccepted, cmd)
}

func parentID(c *gin.Context) *uuid.UUID {
	if p := parentOf(c); p != nil {
		return &p.ID
	}
	return nil
}

// validDay accepts the YYYY-MM-DD form the schema stores, and rejects anything Postgres would
// interpret loosely — "17-08-2026" is a date to Postgres, just not the one the caller meant.
func validDay(v string) bool {
	if len(v) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}
