package e2e

// What the server refuses, and what it says when it refuses.
//
// The journeys prove the product works when everything is right. This file is the other half: every
// control that exists to stop something is exercised from outside, with a positive control beside it
// so that a refusal is never confused with an inability to construct the request in the first place.
//
// Three rules run through the whole file:
//
//   - Every negative control is one field away from a request that works. A bad token built by a
//     different route than the good one can pass while agreeing with a bug in the route it does not
//     share, so each bad token here is the good token with exactly one thing changed.
//   - Where the API is deliberately vague — sign-in answers every failure identically — the test
//     asserts both halves: the answer is byte-identical to the caller, *and* the server recorded the
//     distinguishing reason. Vague without the record is an outage nobody can diagnose; a record
//     without vagueness is an oracle.
//   - A refusal that is refused for the wrong reason is a false green. So the tables assert the
//     status, the machine-readable code and the human message together — a 400 that says "not valid
//     JSON" when the body was perfectly valid and merely too large is a bug this file must catch.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---- local helpers ---------------------------------------------------------

// errorEnvelope is the API's single error shape, written out here rather than imported.
type errorEnvelope struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func envelopeOf(t *testing.T, r apiResponse) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(r.Body, &env); err != nil {
		t.Fatalf("%s %s: error body is not the standard envelope: %v\nbody: %s",
			r.method, r.path, err, r.Body)
	}
	return env
}

// awaitCount waits for the server to have logged sub exactly want times.
//
// Polling rather than reading once: the log line is written before the response is flushed, but this
// process reads the server's stdout through a pipe on another goroutine, so "the response arrived"
// does not imply "the line has been scanned". Asserting on an unpolled count would be flaky in the
// direction that hides a missing log line.
func awaitCount(t *testing.T, h *harness, sub string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := 0
	for time.Now().Before(deadline) {
		got = strings.Count(h.logs.String(), sub)
		if got >= want {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != want {
		t.Fatalf("the server logged %q %d times, expected %d", sub, got, want)
	}
}

// reason renders the slog key/value pair the server writes for a refusal.
func reason(v string) string { return `"reason":"` + v + `"` }

// preFR29Key is what SESSION_SIGNING_KEY was in this suite before FR-29 replaced signed session
// tokens with sign-in rows. A token minted with it is exactly what a console signed in before the
// change still holds: a well-formed JWT, with a valid signature under the key the server used to
// have, that must now be refused like any other string.
const preFR29Key = "e2e-session-signing-key-0123456789-abcdef"

// hs256 mints a session token the way the server did before FR-29, with crypto/hmac.
func hs256(t *testing.T, key string, claims map[string]any) string {
	t.Helper()
	signing := b64(mustJSON(t, map[string]any{"alg": "HS256", "typ": "JWT"})) + "." + b64(mustJSON(t, claims))
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signing))
	return signing + "." + b64(mac.Sum(nil))
}

// sessionClaims is the claim set the pre-FR-29 issuer produced.
func sessionClaims(parentID, email, role string, exp time.Time) map[string]any {
	return map[string]any{
		"sub":   parentID,
		"email": email,
		"role":  role,
		"iat":   time.Now().Add(-time.Minute).Unix(),
		"exp":   exp.Unix(),
	}
}

// changeChar returns the token with the character at i replaced by a different one. A sign-in token
// is compared by the hash of its exact spelling, so — unlike a signature, where two spellings could
// decode to the same bytes — any change to any character is a different credential.
func changeChar(t *testing.T, token string, i int) string {
	t.Helper()
	if i < 0 || i >= len(token) {
		t.Fatalf("no character %d in a %d-character token", i, len(token))
	}
	c := byte('A')
	if token[i] == c {
		c = 'B'
	}
	return token[:i] + string(c) + token[i+1:]
}

func cookiesOf(r apiResponse) []*http.Cookie {
	return (&http.Response{Header: r.Header}).Cookies()
}

func cookieNamed(r apiResponse, name string) *http.Cookie {
	for _, c := range cookiesOf(r) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// refusal is one row of a table: a request, and the exact refusal it must produce.
type refusal struct {
	what   string
	method string
	path   string
	token  string
	body   any
	status int
	code   string
	// says lists fragments the human-readable message must contain. A refusal whose message does not
	// name the field that was wrong sends a parent hunting through a console that looks fine.
	says []string
}

func (h *harness) mustRefuse(t *testing.T, cases []refusal) {
	t.Helper()
	for _, tc := range cases {
		resp := h.call(tc.method, tc.path, tc.token, tc.body)
		if resp.Status != tc.status || resp.errorCode() != tc.code {
			t.Fatalf("%s: expected %d/%s, got %d/%s\nbody: %s",
				tc.what, tc.status, tc.code, resp.Status, resp.errorCode(), resp.Body)
		}
		env := envelopeOf(t, resp)
		for _, want := range tc.says {
			if !strings.Contains(env.Message, want) {
				t.Fatalf("%s: the message does not mention %q: %q", tc.what, want, env.Message)
			}
		}
		if env.RequestID == "" {
			t.Fatalf("%s: the refusal carries no request id, so a parent reporting it cannot be traced", tc.what)
		}
	}
}

// ---- NFR-1: the ID token is verified, never trusted -------------------------

func TestIDTokenIsVerifiedNotTrusted(t *testing.T) {
	h := newHarness(t)
	is := h.issuer

	// The positive control comes first. Every refusal below is one field away from this token, so a
	// suite that could not sign in at all would fail here rather than reporting a perfect score for
	// rejecting tokens it was never able to make valid.
	session := h.signIn(primaryParent)
	if session.Parent.Role != "PRIMARY_ADMIN" {
		t.Fatalf("the first bootstrap address is the primary admin, got %q", session.Parent.Role)
	}

	unverified := primaryParent
	unverified.Verified = false

	bad := []struct {
		what  string
		token string
	}{
		{"not a JWT at all", "this-is-not-a-token"},
		{"three segments of noise", "aGVhZGVy.Y2xhaW1z.c2ln"},
		{"an audience belonging to someone else",
			is.mint(tokenSpec{Who: primaryParent, Audience: "some-other-client.apps.example.test"})},
		{"no audience", is.mint(tokenSpec{Who: primaryParent, Audience: ""})},
		{"an issuer this server does not trust",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, Issuer: "https://evil.example.test"})},
		{"expired five minutes ago, well past the leeway",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, Expires: time.Now().Add(-5 * time.Minute)})},
		{"no expiry at all",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, OmitExpiry: true})},
		{"issued in the future",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, IssuedAt: time.Now().Add(5 * time.Minute)})},
		{"not valid until the future",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, NotBefore: time.Now().Add(5 * time.Minute)})},
		{"alg none, the classic",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, Alg: "none"})},
		{"signed by a key the provider never published, under the published key's id",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, SignWith: is.impostor})},
		{"a key id that is not in the JWKS",
			is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, KID: impostorKID})},
		{"no subject", is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, OmitSubject: true})},
		{"no email", is.mint(tokenSpec{Who: primaryParent, Audience: e2eClientID, OmitEmail: true})},
		{"an email the provider has not verified", is.mint(tokenSpec{Who: unverified, Audience: e2eClientID})},
	}

	// The refusal a caller sees must be identical for every one of them, including for a perfectly
	// good token belonging to somebody who is not a parent here. Anything else turns this endpoint
	// into an oracle for which addresses belong to the family.
	var canonical errorEnvelope
	checkBody := func(what string, body any) {
		resp := h.call(http.MethodPost, "/auth/google", "", body)
		resp.expectError(http.StatusUnauthorized, "unauthorized")
		env := envelopeOf(t, resp)
		if env.Message != "sign-in failed" {
			t.Fatalf("%s: message was %q, expected the same vague %q every other refusal uses",
				what, env.Message, "sign-in failed")
		}
		if canonical.Error == "" {
			canonical = env
		}
		if env.Error != canonical.Error || env.Message != canonical.Message {
			t.Fatalf("%s: refusal differs from the others (%+v vs %+v); the difference is an oracle",
				what, env, canonical)
		}
		if env.RequestID == "" || env.RequestID == canonical.RequestID && canonical.RequestID == "" {
			t.Fatalf("%s: no request id, so the server-side reason cannot be found again", what)
		}
	}
	check := func(what, token string) { checkBody(what, map[string]any{"id_token": token}) }

	check("an empty id_token", "")
	// An absent field, not the two-character string "{}" — sending that as the token value is a
	// token like any other and lands in the verifier, which is a different code path than the one
	// this case is meant to reach.
	checkBody("no id_token field at all", map[string]any{})
	for _, tc := range bad {
		check(tc.what, tc.token)
	}
	check("a valid token for somebody who is not a parent here",
		is.validToken(e2eClientID, stranger))

	// The server answered identically every time; it must not have *decided* identically. These
	// counts are the evidence that the vagueness is a presentation choice and not a verifier that
	// gave up early.
	awaitCount(t, h, reason("no id token"), 2)
	awaitCount(t, h, reason("token rejected"), len(bad))
	awaitCount(t, h, reason("not a parent of this family"), 1)

	// A body that is not an object at all is a client bug, not a sign-in attempt, and is reported as
	// one — the endpoint stays vague about credentials, never about syntax.
	h.mustRefuse(t, []refusal{{
		what: "an id_token of the wrong type", method: http.MethodPost, path: "/auth/google",
		body: `{"id_token": 42}`, status: http.StatusBadRequest, code: "invalid_body",
	}})

	// And the token that worked at the top still works at the bottom: nothing above put the verifier
	// into a state where it refuses everything, which is the failure mode that would make all of the
	// assertions above pass for the wrong reason.
	h.signIn(primaryParent)
}

// ---- NFR-1: sign-in tokens, roles, and what a guessed one cannot buy -------

func TestSessionTokensAreForgeryResistant(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	admin := h.signIn(secondParent)

	guardianIdentity := identity{
		Email: "guardian@family.test", Subject: "google-guardian", Name: "Guardian", Verified: true,
	}
	var created parentDTO
	h.call(http.MethodPost, "/parents", primary.Token, map[string]any{
		"email": guardianIdentity.Email, "role": "GUARDIAN",
	}).expect(http.StatusCreated).decode(&created)
	guardian := h.signIn(guardianIdentity)
	if guardian.Parent.Role != "GUARDIAN" {
		t.Fatalf("the new parent signed in as %q, expected GUARDIAN", guardian.Parent.Role)
	}

	// Calibration for everything below: the token the server handed out works and names its parent.
	// So each refusal that follows is about the string, never about the suite holding no working
	// credential at all.
	var whoami parentDTO
	h.call(http.MethodGet, "/me", primary.Token, nil).expect(http.StatusOK).decode(&whoami)
	if whoami.ID != primary.Parent.ID || whoami.Role != "PRIMARY_ADMIN" {
		t.Fatalf("the real token identified %s as %q, expected %s PRIMARY_ADMIN", whoami.ID, whoami.Role, primary.Parent.ID)
	}

	// A sign-in that has been ended, held by the suite exactly as a stolen copy would be.
	ended := h.signIn(primaryParent)
	h.call(http.MethodDelete, "/sign-ins/current", ended.Token, nil).expect(http.StatusNoContent)

	valid := time.Now().Add(time.Hour)
	rejected := []struct {
		what  string
		token string
	}{
		{"a random token of the right shape that is nobody's", func() string {
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				t.Fatal(err)
			}
			return base64.RawURLEncoding.EncodeToString(buf)
		}()},
		{"a real token with its first character changed", changeChar(t, primary.Token, 0)},
		{"a real token with its last character changed", changeChar(t, primary.Token, len(primary.Token)-1)},
		{"a real token with a character added", primary.Token + "A"},
		{"a real token with its last character dropped", primary.Token[:len(primary.Token)-1]},
		{"a sign-in that was ended", ended.Token},
		// What every console signed in before FR-29 holds: a correctly signed JWT under the key the
		// server used to have. Refused like any other string — the one forced sign-in after the
		// change, and the proof that no JWT verifier is left in the path.
		{"a session token from before FR-29, validly signed with the old key",
			hs256(t, preFR29Key, sessionClaims(primary.Parent.ID, primary.Parent.Email, "PRIMARY_ADMIN", valid))},
		{"the device's own credential, on a parent route", func() string {
			f := enrolledFixture(t, h)
			return f.deviceToken()
		}()},
		{"nothing at all", ""},
	}
	// Errorf rather than Fatalf, and the row's own name in every message: stopping at the first names
	// whichever row happens to be first and hides the rest. That is also what makes this loop
	// calibratable — the break has to be *seen* on the row it is aimed at.
	for _, tc := range rejected {
		resp := h.call(http.MethodGet, "/me", tc.token, nil)
		if resp.Status != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d\nbody: %s", tc.what, resp.Status, resp.Body)
			continue
		}
		if code := resp.errorCode(); code != "unauthorized" {
			t.Errorf("%s: expected error code %q, got %q\nbody: %s", tc.what, "unauthorized", code, resp.Body)
			continue
		}
		if msg := envelopeOf(t, resp).Message; msg != "authentication required" {
			t.Errorf("%s: message was %q", tc.what, msg)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	// And the refusals cost the real sign-in nothing: none of the near-misses above ended it.
	h.call(http.MethodGet, "/me", primary.Token, nil).expect(http.StatusOK)

	// A malformed Authorization header is unauthenticated, not a 500 and not a partial parse.
	for _, header := range []string{"Bearer", "Bearer ", "Basic " + primary.Token, primary.Token} {
		req := h.newRequest(http.MethodGet, "/me", "", nil)
		req.Header.Set("Authorization", header)
		h.send(req).expectError(http.StatusUnauthorized, "unauthorized")
	}
	// …and the header the server does accept is case-insensitive in the scheme, which is what an
	// HTTP client is allowed to send. Without this the loop above would pass on a server that
	// accepted nothing.
	req := h.newRequest(http.MethodGet, "/me", "", nil)
	req.Header.Set("Authorization", "bearer "+primary.Token)
	h.send(req).expect(http.StatusOK)

	// A parent token is not a device token either. Both directions, because a single-sided check
	// would pass on a server that treated every bearer token as both.
	h.call(http.MethodGet, "/device/policy", primary.Token, nil).
		expectError(http.StatusUnauthorized, "unauthorized")

	// Roles are additive and the database is the authority (FR-13). GUARDIAN and ADMIN may use the
	// console; neither may change who else can.
	for _, tc := range []struct {
		who   string
		token string
	}{{"a guardian", guardian.Token}, {"an admin", admin.Token}} {
		h.call(http.MethodGet, "/children", tc.token, nil).expect(http.StatusOK)
		resp := h.call(http.MethodPost, "/parents", tc.token, map[string]any{
			"email": "someone-else@family.test", "role": "ADMIN",
		})
		resp.expectError(http.StatusForbidden, "forbidden")
		if msg := envelopeOf(t, resp).Message; msg != "your role may not do that" {
			t.Fatalf("%s: message was %q", tc.who, msg)
		}
	}

	// A primary admin cannot lock themselves out.
	h.call(http.MethodDelete, "/parents/"+primary.Parent.ID, primary.Token, nil).
		expectError(http.StatusConflict, "conflict")
	h.call(http.MethodGet, "/me", primary.Token, nil).expect(http.StatusOK)

	// Removing a parent takes effect on their next request, not when their token expires.
	h.call(http.MethodGet, "/me", guardian.Token, nil).expect(http.StatusOK)
	h.call(http.MethodDelete, "/parents/"+guardian.Parent.ID, primary.Token, nil).
		expect(http.StatusNoContent)
	h.call(http.MethodGet, "/me", guardian.Token, nil).
		expectError(http.StatusUnauthorized, "unauthorized")

	// Bad input on the parent routes names what was wrong.
	h.mustRefuse(t, []refusal{
		{what: "an email that is not one", method: http.MethodPost, path: "/parents", token: primary.Token,
			body:   map[string]any{"email": "not-an-email", "role": "ADMIN"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"not an email address"}},
		{what: "a role that does not exist", method: http.MethodPost, path: "/parents", token: primary.Token,
			body:   map[string]any{"email": "someone@family.test", "role": "SUPERUSER"},
			status: http.StatusBadRequest, code: "invalid_input",
			says: []string{"PRIMARY_ADMIN", "ADMIN", "GUARDIAN"}},
		{what: "an address that is already a parent", method: http.MethodPost, path: "/parents", token: primary.Token,
			body:   map[string]any{"email": secondParent.Email, "role": "ADMIN"},
			status: http.StatusConflict, code: "conflict"},
	})

	// A parent listing must never carry the identifiers that tie a family member to their Google
	// account. They are json:"-" on the server; this is the assertion from outside.
	var list struct {
		Parents []parentDTO `json:"parents"`
	}
	h.call(http.MethodGet, "/parents", primary.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Parents) == 0 {
		t.Fatal("the parent listing is empty")
	}
	for _, p := range list.Parents {
		if p.GoogleSub != "" || p.FRPAccount != "" {
			t.Fatalf("parent %s exposes provider identifiers: sub=%q frp=%q", p.Email, p.GoogleSub, p.FRPAccount)
		}
	}
}

// ---- FR-1.4: enrolment credentials are single use ---------------------------

func TestEnrollmentCredentialsAreSingleUse(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nia")
	device := h.newDevice(parent.Token, child.ID, "Nia's phone")

	_, first := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(first, "Pixel 6a", "Android 14", nil)
	if enrolled.DeviceID != device.ID {
		t.Fatalf("enrolment bound the token to %s, expected %s", enrolled.DeviceID, device.ID)
	}

	replay := h.call(http.MethodPost, "/enroll", "", map[string]any{"enrollment_token": first})
	replay.expectError(http.StatusConflict, "conflict")
	unknown := h.call(http.MethodPost, "/enroll", "", map[string]any{
		"enrollment_token": "GA5X-QQQQ-NEVER-ISSUED-0000",
	})
	unknown.expectError(http.StatusConflict, "conflict")

	// Identical, by construction: single use is enforced by clearing the hash inside the UPDATE that
	// matches it, so a replay and a token that never existed are the same zero rows. Asserting the
	// sameness is what stops a future "helpful" message from telling a stranger which QR codes were
	// real.
	if got, want := envelopeOf(t, replay).Message, envelopeOf(t, unknown).Message; got != want {
		t.Fatalf("a replayed token says %q and an unknown one says %q; the difference is an oracle", got, want)
	}

	// Minting again invalidates the previous credential at the moment the parent looks at the new QR,
	// not when the old one expires. `reprovision` and not `provision`: this device is enrolled now,
	// and FR-1.7 makes the server refuse a code for an enrolled phone unless the caller says it
	// means to replace it. Which is exactly what these two calls are doing.
	_, second := h.reprovision(parent.Token, device.ID)
	_, third := h.reprovision(parent.Token, device.ID)
	if second == third {
		t.Fatal("two provisioning calls produced the same enrollment token; it is not being re-minted")
	}
	h.call(http.MethodPost, "/enroll", "", map[string]any{"enrollment_token": second}).
		expectError(http.StatusConflict, "conflict")
	// The newest one still works, so the refusal above was about supersession and not about the
	// device having already enrolled.
	h.enrollDevice(third, "Pixel 6a", "Android 14", nil)

	h.mustRefuse(t, []refusal{
		{what: "an empty enrollment token", method: http.MethodPost, path: "/enroll",
			body:   map[string]any{"enrollment_token": "  "},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"enrollment_token is required"}},
		{what: "no enrollment token field", method: http.MethodPost, path: "/enroll",
			body:   map[string]any{"model": "Pixel"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"enrollment_token is required"}},
	})

	// A recovery code exists only after enrolment, and the refusal says so rather than reporting an
	// empty code that a parent would write down.
	fresh := h.newDevice(parent.Token, child.ID, "Not yet provisioned")
	h.mustRefuse(t, []refusal{
		{what: "a recovery code before enrolment", method: http.MethodGet,
			path: "/devices/" + fresh.ID + "/recovery-code", token: parent.Token,
			status: http.StatusNotFound, code: "not_found", says: []string{"generated when the device enrolls"}},
		{what: "a command to a device that never enrolled", method: http.MethodPost,
			path: "/devices/" + fresh.ID + "/commands", token: parent.Token,
			body:   map[string]any{"type": "LOCK_NOW"},
			status: http.StatusConflict, code: "conflict", says: []string{"has not enrolled yet"}},
	})
}

// ---- NFR-1: one device cannot act on another --------------------------------

func TestOneDeviceCannotActOnAnother(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	other := h.newDevice(f.parent.Token, f.child.ID, "Sibling phone")
	_, otherToken := h.provision(f.parent.Token, other.ID)
	otherEnrolled := h.enrollDevice(otherToken, "Pixel 7", "Android 14", nil)

	cmd := h.issueCommand(f.parent.Token, f.device.ID, "LOCK_NOW", nil)

	// The second device polls first. It must see nothing — and, crucially, its poll must not mark the
	// first device's command delivered, which is what a query missing the device id in its WHERE
	// clause would do.
	var queue struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/device/commands", otherEnrolled.DeviceToken, nil).
		expect(http.StatusOK).decode(&queue)
	for _, c := range queue.Commands {
		if c.ID == cmd.ID {
			t.Fatalf("device %s was handed device %s's command", otherEnrolled.DeviceID, f.device.ID)
		}
	}
	if state := h.commandState(f.parent.Token, f.device.ID, cmd.ID); state != "QUEUED" {
		t.Fatalf("another device's poll moved the command to %s; only the owning device's fetch may", state)
	}

	// Acknowledging it is refused as a missing record rather than as a permission error: the second
	// device has no business learning that the id exists.
	h.call(http.MethodPost, "/device/commands/"+cmd.ID+"/ack", otherEnrolled.DeviceToken,
		map[string]any{"ok": true}).expectError(http.StatusNotFound, "not_found")

	// The owning device can do both, which is what proves the two refusals above were about ownership
	// and not about a command that was never ackable.
	h.call(http.MethodGet, "/device/commands", f.deviceToken(), nil).expect(http.StatusOK).decode(&queue)
	if !hasCommand(queue.Commands, cmd.ID) {
		t.Fatalf("the owning device was not handed its own command: %+v", queue.Commands)
	}
	h.call(http.MethodPost, "/device/commands/"+cmd.ID+"/ack", f.deviceToken(),
		map[string]any{"ok": true}).expect(http.StatusOK)
	if state := h.commandState(f.parent.Token, f.device.ID, cmd.ID); state != "ACKED" {
		t.Fatalf("after the owning device acked, the command is %s", state)
	}

	// A device token is not a parent token, in either direction.
	h.call(http.MethodGet, "/devices", f.deviceToken(), nil).
		expectError(http.StatusUnauthorized, "unauthorized")
	h.call(http.MethodPost, "/device/heartbeat", f.parent.Token, map[string]any{}).
		expectError(http.StatusUnauthorized, "unauthorized")
}

func hasCommand(list []commandDTO, id string) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}

// ---- every malformed request is refused with a reason -----------------------

func TestMalformedRequestsAreRefusedWithAReason(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	parent, child, device, dev := f.parent.Token, f.child.ID, f.device.ID, f.deviceToken()

	// The positive control for the policy table below: a patch this shape is accepted, so every
	// refusal that follows is about the values and not about the request being unreadable.
	h.patchPolicy(parent, child, map[string]any{"daily_limit_minutes": 60})

	h.mustRefuse(t, []refusal{
		// Shape.
		{what: "a field the server does not implement", method: http.MethodPost, path: "/children", token: parent,
			body: `{"nickname":"Bug"}`, status: http.StatusBadRequest, code: "invalid_body",
			says: []string{"nickname"}},
		{what: "truncated JSON", method: http.MethodPost, path: "/children", token: parent,
			body: `{"name":`, status: http.StatusBadRequest, code: "invalid_body"},
		{what: "an empty body where one is required", method: http.MethodPost, path: "/children", token: parent,
			body: ``, status: http.StatusBadRequest, code: "invalid_body"},
		{what: "an id that is not a uuid", method: http.MethodGet, path: "/children/not-a-uuid/policy",
			token: parent, status: http.StatusBadRequest, code: "invalid_id", says: []string{"not-a-uuid"}},
		{what: "a child_id filter that is not a uuid", method: http.MethodGet, path: "/devices?child_id=nope",
			token: parent, status: http.StatusBadRequest, code: "invalid_id"},
		{what: "an endpoint that does not exist", method: http.MethodGet, path: "/nope", token: parent,
			status: http.StatusNotFound, code: "not_found", says: []string{"no such endpoint"}},
		{what: "a well-formed id that is nobody's", method: http.MethodGet,
			path: "/devices/11111111-2222-3333-4444-555555555555", token: parent,
			status: http.StatusNotFound, code: "not_found"},

		// Names.
		{what: "a child with no name", method: http.MethodPost, path: "/children", token: parent,
			body: map[string]any{"name": "   "}, status: http.StatusBadRequest, code: "invalid_input",
			says: []string{"a child needs a name"}},
		{what: "a device with no name", method: http.MethodPost, path: "/children/" + child + "/devices",
			token: parent, body: map[string]any{"name": ""}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"a device needs a name"}},

		// Policy, one problem at a time so each message is pinned to its own field.
		{what: "a daily limit longer than a day", method: http.MethodPatch,
			path: "/children/" + child + "/policy", token: parent,
			body: map[string]any{"daily_limit_minutes": 1441}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"between 0 and 1440 minutes"}},
		{what: "a negative daily limit", method: http.MethodPatch,
			path: "/children/" + child + "/policy", token: parent,
			body: map[string]any{"daily_limit_minutes": -1}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"between 0 and 1440 minutes"}},
		{what: "a bedtime that is not a time", method: http.MethodPatch,
			path: "/children/" + child + "/policy", token: parent,
			body: map[string]any{"bedtime_start": "9pm"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"bedtime_start must look like 21:00"}},
		{what: "an hour that does not exist", method: http.MethodPatch,
			path: "/children/" + child + "/policy", token: parent,
			body: map[string]any{"bedtime_end": "25:00"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"bedtime_end must look like 21:00"}},
		{what: "a time zone that is not in the database", method: http.MethodPatch,
			path: "/children/" + child + "/policy", token: parent,
			body: map[string]any{"timezone": "Mars/Olympus"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"unknown time zone Mars/Olympus"}},

		// App rules and domains.
		{what: "an app rule with no package", method: http.MethodPut,
			path: "/children/" + child + "/app-rules", token: parent,
			body: map[string]any{"package_name": " ", "action": "BLOCK"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"package_name is required"}},
		{what: "an app rule with an invented action", method: http.MethodPut,
			path: "/children/" + child + "/app-rules", token: parent,
			body: map[string]any{"package_name": pkgGame, "action": "MAYBE"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"action must be ALLOW, LIMIT, BLOCK or BONUS"}},
		// A per-app allowance on an answer that cannot carry one (FR-5.8). Refused rather than
		// clamped to zero: storing a number that never binds is how a console comes to display a
		// limit nobody is enforcing.
		{what: "a per-app limit on an app that is always free", method: http.MethodPut,
			path: "/children/" + child + "/app-rules", token: parent,
			body:   map[string]any{"package_name": pkgGame, "action": "ALLOW", "limit_minutes": 30},
			status: http.StatusBadRequest,
			code:   "invalid_input", says: []string{"limit_minutes only applies to LIMIT"}},
		{what: "a per-app limit longer than a day", method: http.MethodPut,
			path: "/children/" + child + "/app-rules", token: parent,
			body:   map[string]any{"package_name": pkgGame, "action": "LIMIT", "limit_minutes": 1441},
			status: http.StatusBadRequest,
			code:   "invalid_input", says: []string{"limit_minutes must be between 0 and 1440"}},
		{what: "a negative per-app limit", method: http.MethodPut,
			path: "/children/" + child + "/app-rules", token: parent,
			body:   map[string]any{"package_name": pkgGame, "action": "LIMIT", "limit_minutes": -1},
			status: http.StatusBadRequest,
			code:   "invalid_input", says: []string{"limit_minutes must be between 0 and 1440"}},
		{what: "clearing an app rule without naming it", method: http.MethodDelete,
			path: "/children/" + child + "/app-rules", token: parent, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"package_name is required"}},
		{what: "a domain that is not one", method: http.MethodPost,
			path: "/children/" + child + "/blocked-domains", token: parent,
			body: map[string]any{"domain": "not a domain"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"that is not a domain"}},
		{what: "unblocking without naming a domain", method: http.MethodDelete,
			path: "/children/" + child + "/blocked-domains", token: parent, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"domain is required"}},

		// Commands and previews.
		{what: "a command type nothing implements", method: http.MethodPost,
			path: "/devices/" + device + "/commands", token: parent,
			body: map[string]any{"type": "SELF_DESTRUCT"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"unknown command type SELF_DESTRUCT"}},
		{what: "a preview instant that is not a timestamp", method: http.MethodGet,
			path: "/devices/" + device + "/desired-state?at=tomorrow", token: parent,
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"RFC 3339"}},
		{what: "a usage day in the wrong format", method: http.MethodGet,
			path: "/devices/" + device + "/usage?day=17-08-2026", token: parent,
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"day must look like"}},

		// What a phone sends.
		{what: "a battery level that is not a percentage", method: http.MethodPost,
			path: "/device/heartbeat", token: dev, body: map[string]any{"battery_level": 101},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"battery_level must be 0..100"}},
		{what: "usage filed for tomorrow", method: http.MethodPost, path: "/device/usage", token: dev,
			body:   map[string]any{"day": time.Now().AddDate(0, 0, 2).Format("2006-01-02"), "samples": map[string]int64{pkgGame: 60}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"day is in the future"}},
		{what: "usage filed for last month", method: http.MethodPost, path: "/device/usage", token: dev,
			body:   map[string]any{"day": time.Now().AddDate(0, 0, -30).Format("2006-01-02"), "samples": map[string]int64{pkgGame: 60}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"more than a week old"}},
		{what: "usage filed for a day that is not a date", method: http.MethodPost, path: "/device/usage",
			token: dev, body: map[string]any{"day": "yesterday"}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"day must look like"}},
		{what: "a latitude off the planet", method: http.MethodPost, path: "/device/location", token: dev,
			body: map[string]any{"latitude": 91.0, "longitude": 8.5}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"latitude/longitude out of range"}},
		{what: "a longitude off the planet", method: http.MethodPost, path: "/device/location", token: dev,
			body: map[string]any{"latitude": 47.4, "longitude": -181.0}, status: http.StatusBadRequest,
			code: "invalid_input", says: []string{"latitude/longitude out of range"}},
		{what: "a capture time that is not RFC 3339", method: http.MethodPost, path: "/device/location",
			token: dev, body: map[string]any{"latitude": 47.4, "longitude": 8.5, "captured_at": "just now"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"captured_at must be RFC 3339"}},
		{what: "a recovery event with an unparseable time", method: http.MethodPost,
			path: "/device/recovery-event", token: dev,
			body:   map[string]any{"succeeded": true, "occurred_at": "last tuesday"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"occurred_at must be RFC 3339"}},
	})

	// One patch, four problems, one answer. A server that reported only the first would make a parent
	// fix a form four times.
	resp := h.call(http.MethodPatch, "/children/"+child+"/policy", parent, map[string]any{
		"daily_limit_minutes": 5000,
		"bedtime_start":       "9pm",
		"bedtime_end":         "25:00",
		"timezone":            "Mars/Olympus",
	})
	resp.expectError(http.StatusBadRequest, "invalid_input")
	message := envelopeOf(t, resp).Message
	for _, want := range []string{
		"between 0 and 1440 minutes", "bedtime_start must look like 21:00",
		"bedtime_end must look like 21:00", "unknown time zone Mars/Olympus",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("the combined refusal does not mention %q: %q", want, message)
		}
	}
	if n := strings.Count(message, "; "); n != 3 {
		t.Fatalf("expected four problems joined by three separators, got %d in %q", n, message)
	}

	// Nothing above was written: a refused patch must leave the policy as it was.
	var pol policyDTO
	h.call(http.MethodGet, "/children/"+child+"/policy", parent, nil).expect(http.StatusOK).decode(&pol)
	if pol.DailyLimitMinutes != 60 {
		t.Fatalf("a refused patch changed the daily limit to %d", pol.DailyLimitMinutes)
	}
	if pol.Timezone == "Mars/Olympus" {
		t.Fatal("a refused patch wrote the invalid time zone")
	}
}

// ---- NFR-2: a body cannot be used to exhaust the process --------------------

func TestOversizedBodiesAreRefusedAsTooLarge(t *testing.T) {
	// 1024 is the smallest cap the configuration accepts, which makes the boundary cheap to straddle
	// without sending a megabyte through the socket.
	h := newHarness(t, withEnv("MAX_BODY_BYTES", "1024"))

	// Both requests go to the same endpoint and differ only in size, so the difference in the answer
	// is attributable to the cap and to nothing else. The small one is refused for its *content* —
	// which is the proof that the body was read at all.
	h.mustRefuse(t, []refusal{{
		what: "a body just under the cap", method: http.MethodPost, path: "/enroll",
		body:   map[string]any{"enrollment_token": "", "model": strings.Repeat("A", 850)},
		status: http.StatusBadRequest, code: "invalid_input", says: []string{"enrollment_token is required"},
	}, {
		what: "a body well over the cap", method: http.MethodPost, path: "/enroll",
		body:   map[string]any{"enrollment_token": "", "model": strings.Repeat("A", 4000)},
		status: http.StatusRequestEntityTooLarge, code: "body_too_large",
		says: []string{"exceeds the maximum accepted size"},
	}})

	// The server is still serving afterwards: the cap refuses a request, it does not wedge the
	// process or leave the connection in a state the next request inherits.
	h.call(http.MethodGet, "/healthz", "", nil).expect(http.StatusOK)
}

// ---- NFR-2: the rate limiter ------------------------------------------------

func TestRateLimitProtectsTheServer(t *testing.T) {
	const perMinute = 20
	h := newHarness(t, withEnv("RATE_LIMIT_PER_MINUTE", fmt.Sprint(perMinute)))

	// Burst equals the per-minute allowance, so the bucket empties within a couple of dozen requests
	// and the test never has to wait a minute to observe the limit.
	served, limited := drain(h, "")
	if served == 0 {
		t.Fatal("the very first request was rate limited; the burst is not the per-minute allowance")
	}
	if limited.Status != http.StatusTooManyRequests {
		t.Fatalf("%d requests never hit the limit of %d per minute", served, perMinute)
	}
	limited.expectError(http.StatusTooManyRequests, "rate_limited")
	if got := limited.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After was %q; a client that is told to back off needs to know for how long", got)
	}
	if msg := envelopeOf(t, limited).Message; msg != "too many requests" {
		t.Fatalf("the refusal says %q", msg)
	}

	// A forged forwarding header must not buy a fresh bucket. TRUSTED_PROXIES is empty here, so the
	// client address is the socket peer.
	req := h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	h.send(req).expectError(http.StatusTooManyRequests, "rate_limited")

	// The limit is a delay, not a lockout: at 20/minute a token returns roughly every three seconds,
	// so a client that waits gets served. Without this half, a limiter that never refilled would pass
	// every assertion above.
	deadline := time.Now().Add(15 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if h.call(http.MethodGet, "/healthz", "", nil).Status == http.StatusOK {
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatal("the bucket never refilled; a rate limit that does not recover is an outage")
	}

	// Calibration for the forged-header assertion above. With the loopback declared as a trusted
	// proxy, the same header *does* select a different bucket — so the earlier refusal was about the
	// proxy not being trusted, and not about a limiter that ignores the header in every configuration.
	trusting := newHarness(t,
		withEnv("RATE_LIMIT_PER_MINUTE", fmt.Sprint(perMinute)),
		withEnv("TRUSTED_PROXIES", "127.0.0.1/32"))
	if _, blocked := drain(trusting, ""); blocked.Status != http.StatusTooManyRequests {
		t.Fatal("the calibration server never reached its limit")
	}
	forwarded := trusting.newRequest(http.MethodGet, "/healthz", "", nil)
	forwarded.Header.Set("X-Forwarded-For", "203.0.113.9")
	trusting.send(forwarded).expect(http.StatusOK)
}

// drain sends requests until one is refused, and returns how many were served plus the refusal.
func drain(h *harness, forwardedFor string) (served int, refused apiResponse) {
	for i := 0; i < 200; i++ {
		req := h.newRequest(http.MethodGet, "/healthz", "", nil)
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		resp := h.send(req)
		if resp.Status != http.StatusOK {
			return served, resp
		}
		served++
	}
	return served, refused
}

// ---- NFR-1: CORS admits exactly the configured origins ----------------------

func TestCORSAllowsOnlyTheConfiguredOrigins(t *testing.T) {
	// The second entry carries a trailing slash and mixed case on purpose: the console is configured
	// by a human, and an origin that fails to match because of a slash is a deployment that looks
	// configured and refuses every request from the console.
	h := newHarness(t, withEnv("ALLOWED_ORIGINS", "https://console.example.test, https://Alt.Example.Test/"))

	for _, origin := range []string{"https://console.example.test", "https://alt.example.test"} {
		req := h.newRequest(http.MethodOptions, "/me", "", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp := h.send(req).expect(http.StatusNoContent)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("preflight from %s echoed %q", origin, got)
		}
		if !strings.Contains(resp.Header.Get("Vary"), "Origin") {
			t.Fatalf("preflight from %s did not vary on Origin, so a cache can serve it to another origin", origin)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Fatalf("preflight from %s did not allow credentials (%q), so the console cannot send its token", origin, got)
		}
		// Even a preflight carries the standing security headers.
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("preflight from %s carries no Content-Security-Policy", origin)
		}
	}

	// Every method the console sends must be named in the preflight's answer, or the BROWSER refuses
	// the real request and the parent sees a network error while curl works. PUT was missing until
	// 0.6.37: every plan, alarm, agenda, holiday and app-rule save failed from a cross-origin console.
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		req := h.newRequest(http.MethodOptions, "/me", "", nil)
		req.Header.Set("Origin", "https://console.example.test")
		req.Header.Set("Access-Control-Request-Method", method)
		resp := h.send(req).expect(http.StatusNoContent)
		named := false
		for _, m := range strings.Split(resp.Header.Get("Access-Control-Allow-Methods"), ",") {
			named = named || strings.TrimSpace(m) == method
		}
		if !named {
			t.Fatalf("a preflight for %s answered Allow-Methods %q: a browser refuses the real request",
				method, resp.Header.Get("Access-Control-Allow-Methods"))
		}
	}

	// A preflight that is not allowed is refused rather than answered: a 204 would confirm the
	// endpoint exists and is reachable.
	for _, tc := range []struct {
		what   string
		origin string
	}{
		{"an origin nobody configured", "https://evil.example.test"},
		{"a look-alike host", "https://console.example.test.evil.test"},
		{"a plaintext version of an allowed origin", "http://console.example.test"},
		{"no origin at all", ""},
	} {
		req := h.newRequest(http.MethodOptions, "/me", "", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp := h.send(req)
		if resp.Status != http.StatusForbidden {
			t.Fatalf("%s: preflight answered %d, expected 403", tc.what, resp.Status)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: refused preflight still carried Access-Control-Allow-Origin: %q", tc.what, got)
		}
	}

	// A real request from a disallowed origin is answered — CORS is a browser mechanism, not an
	// authorization one — but without the header that would let a page read the answer.
	req := h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("Origin", "https://evil.example.test")
	resp := h.send(req).expect(http.StatusOK)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("a disallowed origin was told it may read the response: %q", got)
	}

	// The wildcard has no branch in the code and no way into the configuration. This is the assertion
	// that says so from outside.
	req = h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("Origin", "https://console.example.test")
	resp = h.send(req).expect(http.StatusOK)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://console.example.test" {
		t.Fatalf("an allowed origin got %q", got)
	}
}

// ---- NFR-1: the security headers hold on every answer -----------------------

func TestSecurityHeadersOnEveryAnswer(t *testing.T) {
	h := newHarness(t)

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         "geolocation=(), camera=(), microphone=(), payment=()",
	}

	// A rejected request is exactly where these are easiest to lose: the middleware that sets them
	// runs before routing, and an abort inside an earlier layer would skip them.
	for _, tc := range []struct {
		what string
		resp apiResponse
	}{
		{"a 404", h.call(http.MethodGet, "/nope", "", nil).expect(http.StatusNotFound)},
		{"a 401", h.call(http.MethodGet, "/me", "", nil).expect(http.StatusUnauthorized)},
		{"a 200", h.call(http.MethodGet, "/healthz", "", nil).expect(http.StatusOK)},
	} {
		for header, value := range want {
			if got := tc.resp.Header.Get(header); got != value {
				t.Fatalf("%s: %s was %q, expected %q", tc.what, header, got, value)
			}
		}
		csp := tc.resp.Header.Get("Content-Security-Policy")
		for _, directive := range []string{
			"default-src 'self'", "script-src 'self'", "object-src 'none'",
			"base-uri 'none'", "frame-ancestors 'none'",
		} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("%s: the policy is missing %q: %q", tc.what, directive, csp)
			}
		}
		for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "ws:", "*"} {
			if strings.Contains(csp, forbidden) {
				t.Fatalf("%s: the policy contains %q: %q", tc.what, forbidden, csp)
			}
		}
		if tc.resp.Header.Get("X-Request-Id") == "" {
			t.Fatalf("%s: no request id was echoed", tc.what)
		}
	}

	// HSTS, both halves. Over plaintext it must be absent: promising a year of https from a
	// developer's http://localhost pins that host in the browser for a year.
	plain := h.call(http.MethodGet, "/healthz", "", nil).expect(http.StatusOK)
	if got := plain.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS was sent over plaintext: %q", got)
	}
	// Behind the cluster ingress, TLS is terminated one hop out and the header is how this process
	// learns of it.
	req := h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	secured := h.send(req).expect(http.StatusOK)
	if got := secured.Header.Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
		t.Fatalf("behind a TLS-terminating proxy, HSTS was %q", got)
	}

	// A caller-supplied request id is echoed so a parent's report can be matched to a log line — but
	// only within a sane length, so it cannot be used to write arbitrary bytes into every log.
	req = h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("X-Request-Id", "parent-report-42")
	if got := h.send(req).Header.Get("X-Request-Id"); got != "parent-report-42" {
		t.Fatalf("a caller-supplied request id came back as %q", got)
	}
	req = h.newRequest(http.MethodGet, "/healthz", "", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("x", 200))
	if got := h.send(req).Header.Get("X-Request-Id"); got == strings.Repeat("x", 200) || got == "" {
		t.Fatalf("an over-long request id was echoed verbatim: %q", got)
	}
}

// ---- NFR-1: a misconfigured deployment does not start -----------------------

// startServer runs the real binary with exactly this environment and reports how it exited.
//
// newHarness cannot be used here: it fails the test when the server does not come up, which is the
// outcome every case below is asserting.
func startServer(t *testing.T, env map[string]string) (code int, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, serverBin)
	cmd.Env = envSlice(env)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run the server: %v", err)
	}
	return exit.ExitCode(), string(out)
}

func TestStartupRefusesBadConfiguration(t *testing.T) {
	certPath, _ := writeAPKCert(t)
	// A database nothing is listening on. Every case below is expected to fail *before* this matters;
	// the positive control at the end is the one that proves it.
	deadDB := fmt.Sprintf("postgres://user:pass@127.0.0.1:%d/nothing?sslmode=disable", freePort(t))

	base := func() map[string]string {
		return map[string]string{
			"ADDR":                    "127.0.0.1:0",
			"PUBLIC_URL":              "http://127.0.0.1:8080",
			"DATABASE_URL":            deadDB,
			"OAUTH_CLIENT_ID":         e2eClientID,
			"OAUTH_CLIENT_SECRET":     e2eClientSecret,
			"BOOTSTRAP_PARENT_EMAILS": primaryParent.Email,
			"APK_URL":                 "https://apk.example.test/familyguard.apk",
			"APK_CERT_PATH":           certPath,
			"LOG_LEVEL":               "error",
		}
	}

	cases := []struct {
		what   string
		mutate func(map[string]string)
		says   string
	}{
		{"no database", func(e map[string]string) { delete(e, "DATABASE_URL") }, "DATABASE_URL is required"},
		{"no oauth client id", func(e map[string]string) { delete(e, "OAUTH_CLIENT_ID") },
			"OAUTH_CLIENT_ID is required"},
		{"no oauth client secret", func(e map[string]string) { delete(e, "OAUTH_CLIENT_SECRET") },
			"OAUTH_CLIENT_SECRET is required"},
		{"an idle timeout that is not a duration",
			func(e map[string]string) { e["SIGN_IN_IDLE_TIMEOUT"] = "90 days" }, "SIGN_IN_IDLE_TIMEOUT must be a duration"},
		{"an idle timeout under a second",
			func(e map[string]string) { e["SIGN_IN_IDLE_TIMEOUT"] = "500ms" }, "SIGN_IN_IDLE_TIMEOUT must be a duration of at least 1s"},
		{"no public url", func(e map[string]string) { delete(e, "PUBLIC_URL") }, "PUBLIC_URL is required"},
		{"a public url that is only a path",
			func(e map[string]string) { e["PUBLIC_URL"] = "/console" }, "PUBLIC_URL: must be an absolute URL"},
		{"an APK served over plaintext",
			func(e map[string]string) { e["APK_URL"] = "http://apk.example.test/app.apk" },
			`APK_URL: "http://apk.example.test/app.apk" is not https`},
		// PUBLIC_URL becomes server_url in the provisioning payload, so it is held to the same rule.
		// The loopback form is the one the rest of this suite runs on, which is what keeps this case
		// from being satisfiable by a rule that simply refuses everything.
		{"a console reachable only over plaintext",
			func(e map[string]string) { e["PUBLIC_URL"] = "http://guard.example.test" },
			`PUBLIC_URL: "http://guard.example.test" is not https`},
		{"an APK with no checksum source", func(e map[string]string) {
			delete(e, "APK_CERT_PATH")
			delete(e, "APK_PATH")
		}, "neither APK_PATH nor APK_CERT_PATH"},
		{"a signing certificate that is not on disk",
			func(e map[string]string) { e["APK_CERT_PATH"] = "/nonexistent/signing-cert.der" },
			"APK_CERT_PATH:"},
		{"a wildcard origin", func(e map[string]string) { e["ALLOWED_ORIGINS"] = "*" },
			`ALLOWED_ORIGINS must not contain "*"`},
		{"an origin with a path",
			func(e map[string]string) { e["ALLOWED_ORIGINS"] = "https://console.example.test/app" },
			"must be scheme://host[:port] with no path"},
		{"an origin that is only a host",
			func(e map[string]string) { e["ALLOWED_ORIGINS"] = "console.example.test" },
			"must be an absolute URL"},
		{"a trusted proxy that is not an address",
			func(e map[string]string) { e["TRUSTED_PROXIES"] = "ingress-nginx" },
			"is neither an IP nor a CIDR"},
		{"a rate limit of zero",
			func(e map[string]string) { e["RATE_LIMIT_PER_MINUTE"] = "0" },
			"RATE_LIMIT_PER_MINUTE must be positive"},
		{"a rate limit that is not a number",
			func(e map[string]string) { e["RATE_LIMIT_PER_MINUTE"] = "many" },
			"RATE_LIMIT_PER_MINUTE:"},
		{"a body cap small enough to reject every request",
			func(e map[string]string) { e["MAX_BODY_BYTES"] = "10" },
			"MAX_BODY_BYTES must be at least 1024"},
		{"an audit retention of zero",
			func(e map[string]string) { e["AUDIT_RETENTION_DAYS"] = "0" },
			"AUDIT_RETENTION_DAYS must be positive"},
		{"a negative location retention",
			func(e map[string]string) { e["LOCATION_RETENTION_DAYS"] = "-1" },
			"LOCATION_RETENTION_DAYS must be positive"},
		{"an issuer with whitespace in it",
			func(e map[string]string) { e["OIDC_ISSUER"] = "https://accounts.google.com /" },
			"OIDC_ISSUER: must not contain whitespace"},
		{"a JWKS url that is not a url",
			func(e map[string]string) { e["OIDC_JWKS_URL"] = "certs.json" },
			"OIDC_JWKS_URL: must be an absolute URL"},
		{"a bootstrap address that is not an email",
			func(e map[string]string) { e["BOOTSTRAP_PARENT_EMAILS"] = "parent-at-example" },
			"which is not an email address"},
	}

	for _, tc := range cases {
		env := base()
		tc.mutate(env)
		code, out := startServer(t, env)
		switch {
		case code == 0:
			t.Fatalf("%s: the server started and stayed up; it must refuse to serve\n%s", tc.what, out)
		case code != 1:
			t.Fatalf("%s: exit status %d, expected 1\n%s", tc.what, code, out)
		}
		if !strings.Contains(out, tc.says) {
			t.Fatalf("%s: the refusal does not name the problem (%q):\n%s", tc.what, tc.says, out)
		}
		if !strings.Contains(out, "invalid configuration") {
			t.Fatalf("%s: refused without saying the configuration is what was wrong:\n%s", tc.what, out)
		}
		// The one that matters most: it must not have reached the database, or the message a
		// deployer sees would be about connectivity rather than about the variable they got wrong.
		if strings.Contains(out, "fatal: database:") {
			t.Fatalf("%s: validation happened after connecting to the database:\n%s", tc.what, out)
		}
	}

	// Every problem at once, because a deployer should fix them in one pass rather than one restart
	// at a time.
	env := base()
	delete(env, "DATABASE_URL")
	env["SIGN_IN_IDLE_TIMEOUT"] = "soon"
	env["TRUSTED_PROXIES"] = "ingress-nginx"
	code, out := startServer(t, env)
	if code != 1 {
		t.Fatalf("three problems at once exited %d\n%s", code, out)
	}
	for _, want := range []string{
		"DATABASE_URL is required", "SIGN_IN_IDLE_TIMEOUT must be a duration",
		"is neither an IP nor a CIDR",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("only some problems were reported; %q is missing:\n%s", want, out)
		}
	}

	// Positive control. The base environment is valid, so it gets past configuration and dies on the
	// database that is not there — which is what makes every refusal above attributable to the one
	// thing that case changed.
	code, out = startServer(t, base())
	if code != 1 {
		t.Fatalf("the base environment exited %d, expected a database failure\n%s", code, out)
	}
	if !strings.Contains(out, "fatal: database:") {
		t.Fatalf("the base environment failed somewhere other than the database:\n%s", out)
	}
	if strings.Contains(out, "invalid configuration") {
		t.Fatalf("the base environment is itself misconfigured, so the cases above prove nothing:\n%s", out)
	}

	// A deployment with a reachable database and nobody who could ever sign in is refused too. This
	// one has to run against a real database, because it is checked after the migrations.
	orphan := base()
	orphan["DATABASE_URL"] = databaseURL(newDatabase(t))
	delete(orphan, "BOOTSTRAP_PARENT_EMAILS")
	code, out = startServer(t, orphan)
	if code != 1 {
		t.Fatalf("a server nobody can sign in to exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "nobody would be able to sign in") {
		t.Fatalf("the empty-family refusal does not say why:\n%s", out)
	}
}

// ---- FR-2: the browser sign-in fails closed ---------------------------------

func TestBrowserSignInFailureModes(t *testing.T) {
	h := newHarness(t)
	is := h.issuer

	// Somebody with a perfectly good Google account who is not a parent here is told so — they
	// completed a real sign-in, so this tells the person holding the phone something they need and an
	// outsider nothing they could not learn by trying.
	if got := h.browserSignIn(stranger).Get("error"); got != "not_a_parent" {
		t.Fatalf("a stranger's browser sign-in ended with error=%q", got)
	}

	// A provider that misbehaves is a failed sign-in, not a stack trace on the console.
	is.setTokenStatus(http.StatusInternalServerError)
	if got := h.browserSignIn(primaryParent).Get("error"); got != "login_failed" {
		t.Fatalf("a broken token endpoint ended with error=%q", got)
	}
	is.setTokenStatus(0)

	// Drive the handshake by hand from here on, so the callback can be replayed with one thing wrong.
	start := h.call(http.MethodGet, "/auth/google/start", "", nil).expect(http.StatusFound)
	handshake := cookieNamed(start, "fg_oauth")
	if handshake == nil {
		t.Fatalf("the sign-in start set no handshake cookie: %v", start.Header.Values("Set-Cookie"))
	}
	if !handshake.HttpOnly {
		t.Fatal("the handshake cookie is readable by script; it carries the PKCE verifier")
	}
	if handshake.Path != "/api/v1/auth" {
		t.Fatalf("the handshake cookie is scoped to %q, so it rides along on requests that do not need it", handshake.Path)
	}
	if handshake.SameSite != http.SameSiteLaxMode {
		t.Fatalf("the handshake cookie's SameSite is %v; Strict would drop it on the way back from the provider",
			handshake.SameSite)
	}
	if handshake.Secure {
		t.Fatal("the handshake cookie is marked Secure over plaintext, so it can never be sent to this server")
	}
	parts := strings.Split(handshake.Value, ".")
	if len(parts) != 3 {
		t.Fatalf("the handshake cookie has %d parts", len(parts))
	}
	state, verifier := parts[0], parts[1]

	// The provider's redirect back, with the code the callback is supposed to exchange.
	is.setNextLogin(primaryParent)
	challenge := sha256.Sum256([]byte(verifier))
	authorize := is.AuthURL() + "?" + url.Values{
		"client_id":             {e2eClientID},
		"redirect_uri":          {h.base + "/api/v1/auth/google/callback"},
		"response_type":         {"code"},
		"state":                 {state},
		"nonce":                 {"a-nonce-this-handshake-never-asked-for"},
		"code_challenge":        {b64(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()
	bounced := h.call(http.MethodGet, authorize, "", nil).expect(http.StatusFound)
	back, err := url.Parse(bounced.Header.Get("Location"))
	if err != nil {
		t.Fatalf("the provider's redirect is unparseable: %v", err)
	}
	code := back.Query().Get("code")
	if code == "" {
		t.Fatal("the provider's redirect carries no authorization code")
	}

	callback := func(what string, cookie string, query url.Values) apiResponse {
		req := h.newRequest(http.MethodGet, "/auth/google/callback?"+query.Encode(), "", nil)
		if cookie != "" {
			req.Header.Set("Cookie", "fg_oauth="+cookie)
		}
		resp := h.send(req)
		if resp.Status != http.StatusFound {
			t.Fatalf("%s: the callback answered %d, expected a redirect back to the console\nbody: %s",
				what, resp.Status, resp.Body)
		}
		if got := resp.Header.Get("Location"); got != "/#error=login_failed" {
			t.Fatalf("%s: the callback sent the browser to %q", what, got)
		}
		// The handshake is over either way, so the cookie carrying the PKCE verifier is deleted.
		cleared := cookieNamed(resp, "fg_oauth")
		if cleared == nil || cleared.MaxAge >= 0 {
			t.Fatalf("%s: the handshake cookie was not cleared: %+v", what, cleared)
		}
		return resp
	}

	callback("a callback nobody's browser started", "",
		url.Values{"state": {state}, "code": {code}})
	callback("a handshake cookie that is not three parts", "only-one-part",
		url.Values{"state": {state}, "code": {code}})
	callback("a state that does not match the handshake", handshake.Value,
		url.Values{"state": {"not-the-state-we-issued"}, "code": {code}})
	callback("no state at all", handshake.Value, url.Values{"code": {code}})
	callback("a provider that reported an error", handshake.Value,
		url.Values{"state": {state}, "error": {"access_denied"}})
	callback("no authorization code", handshake.Value, url.Values{"state": {state}})
	callback("a code the provider will not exchange", handshake.Value,
		url.Values{"state": {state}, "code": {"never-issued"}})

	// The nonce check, which nothing else in the suite reaches: this code is real, the PKCE verifier
	// matches, the token is signed by the right key for the right audience — and it was minted for a
	// different authorization request. Without this check that token would be replayable into this
	// parent's session.
	callback("an ID token minted for a different authorization request", handshake.Value,
		url.Values{"state": {state}, "code": {code}})

	awaitCount(t, h, reason("no handshake cookie"), 1)
	awaitCount(t, h, reason("malformed handshake cookie"), 1)
	awaitCount(t, h, reason("state mismatch"), 2)
	awaitCount(t, h, reason("provider returned an error: access_denied"), 1)
	awaitCount(t, h, reason("no authorization code"), 1)
	awaitCount(t, h, reason("code exchange failed"), 2)
	awaitCount(t, h, reason("nonce mismatch"), 1)
	// Nothing above was refused because a token failed verification — in particular the stranger, who
	// signed in successfully and was turned away for not being a parent. Read last, after the polls
	// above have given the log every chance to catch up, so the zero is a measurement.
	awaitCount(t, h, reason("token rejected"), 0)

	// None of the above minted a session, and none of them printed a token into the log.
	if strings.Contains(h.logs.String(), "eyJhbGciOiJIUzI1NiI") {
		t.Fatal("a session token was written to the log")
	}

	// The flow still works, so every refusal above was about the one thing that was wrong rather than
	// about a handshake this test broke on the way in.
	result := h.browserSignIn(primaryParent)
	if result.Get("token") == "" {
		t.Fatalf("the browser sign-in no longer works: %v", result)
	}

	// Over https the same cookie is marked Secure. Asserted on a second server rather than by
	// terminating TLS here, because the flag follows PUBLIC_URL's scheme — which is what a deployment
	// behind the cluster ingress actually sets.
	secure := newHarness(t, withEnv("PUBLIC_URL", "https://guard.example.test"))
	c := cookieNamed(secure.call(http.MethodGet, "/auth/google/start", "", nil).
		expect(http.StatusFound), "fg_oauth")
	if c == nil || !c.Secure {
		t.Fatalf("with an https public URL the handshake cookie is not Secure: %+v", c)
	}
}

// oauthStart's "browser sign-in is not configured" branch is deliberately not asserted: config.Load
// refuses to start without OAUTH_CLIENT_SECRET, OAUTH_AUTH_URL and OAUTH_TOKEN_URL, so no running
// server can reach it. It is kept because loginConfigured() is the honest guard for a future
// deployment mode, and a test that pretended to cover it would be asserting nothing.
