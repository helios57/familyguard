package e2e

// FR-16.7 (an application every child has, including one added later) and FR-16.8 (builds that
// come from Google Play rather than from an upload), end to end.
//
// Google is the one party this suite cannot run, so fakePlay stands in for it — and stands in
// strictly: its token endpoint verifies the RS256 signature of the service account's JWT against
// the key's public half, and refuses a grant for any other scope; its API answers the documented
// paths only. The bytes it serves are the same real fixture APKs the catalog tests use, so the
// server parses, pins and serves a genuine archive, exactly as it would Google's universal APK.
//
// The fixtures' signing certificate digest (the debug key's, below) is written out rather than
// derived: a suite that asked the server for it would be checking the server against itself.
// Measured with backend/internal/apk.ParseFile on 2026-10-15.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fixtureSignerSHA256      = "32d7a8778a00785f25f5e7987791067ce809b43dc451ec53301f0ff3af0d7feb"
	otherFixtureSignerSHA256 = "85424ac797268f66b0d361e774fe192b6553896e9e0d16a6f209045eb86e69bc"
	playAndroidPublisher     = "https://www.googleapis.com/auth/androidpublisher"
)

type playSourceDTO struct {
	PackageName      string   `json:"package_name"`
	Tracks           []string `json:"tracks"`
	Label            string   `json:"label"`
	CheckedAt        *string  `json:"checked_at"`
	PublishedVersion *int64   `json:"published_version"`
	LastError        string   `json:"last_error"`
}

type playSourcesDTO struct {
	Configured bool            `json:"configured"`
	Account    string          `json:"account"`
	Sources    []playSourceDTO `json:"sources"`
}

type playSyncDTO struct {
	Checked    int               `json:"checked"`
	Registered []appDTO          `json:"registered"`
	Errors     map[string]string `json:"errors"`
}

type familyManagedAppDTO struct {
	PackageName    string  `json:"package_name"`
	Available      bool    `json:"available"`
	App            *appDTO `json:"app"`
	ForAllChildren bool    `json:"for_all_children"`
}

// playBuild is one release the fake publishes: a version on a track, in a lifecycle state, served
// from a fixture file and described as signed by a certificate.
type playBuild struct {
	track   string
	version int64
	state   string
	file    string
	signer  string
}

type fakePlay struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	builds map[string][]playBuild // package -> releases
	tokens int
	denied bool
}

func newFakePlay(t *testing.T) *fakePlay {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePlay{t: t, key: key, builds: map[string][]playBuild{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePlay) publish(pkg string, b playBuild) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds[pkg] = append(f.builds[pkg], b)
}

// promote moves a release to a new lifecycle state, as Google does when its review finishes.
func (f *fakePlay) promote(pkg string, version int64, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.builds[pkg] {
		if f.builds[pkg][i].version == version {
			f.builds[pkg][i].state = state
		}
	}
}

// serviceAccount is the key the server is configured with, base64 like the secret store holds it.
func (f *fakePlay) serviceAccount() string {
	der, _ := x509.MarshalPKCS8PrivateKey(f.key)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "play-e2e", "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "reader@play-e2e.iam.gserviceaccount.com", "client_id": "1",
		"token_uri": f.srv.URL + "/token",
	})
	return base64.StdEncoding.EncodeToString(sa)
}

func (f *fakePlay) options() []harnessOption {
	return []harnessOption{
		withEnv("PLAY_SERVICE_ACCOUNT", f.serviceAccount()),
		withEnv("PLAY_ENDPOINT", f.srv.URL),
	}
}

func (f *fakePlay) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || !f.validAssertion(r.Form.Get("assertion")) {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"play-e2e-token","token_type":"Bearer","expires_in":3600}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer play-e2e-token" {
		http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"no token"}}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	denied := f.denied
	f.mu.Unlock()
	if denied {
		http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"The caller does not have permission"}}`, http.StatusForbidden)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/androidpublisher/v3/applications/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	for i := range parts {
		parts[i], _ = url.PathUnescape(parts[i])
	}
	f.mu.Lock()
	builds := append([]playBuild(nil), f.builds[parts[0]]...)
	f.mu.Unlock()
	switch {
	case len(parts) == 4 && parts[1] == "tracks" && parts[3] == "releases":
		type artifact struct {
			VersionCode int64 `json:"versionCode"`
		}
		type release struct {
			Name      string     `json:"releaseName"`
			Artifacts []artifact `json:"activeArtifacts"`
			State     string     `json:"releaseLifecycleState"`
			Track     string     `json:"track"`
		}
		out := struct {
			Releases []release `json:"releases,omitempty"`
		}{}
		for _, b := range builds {
			if b.track == parts[2] {
				out.Releases = append(out.Releases, release{fmt.Sprint(b.version), []artifact{{b.version}}, b.state, b.track})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case len(parts) == 3 && parts[1] == "generatedApks":
		for _, b := range builds {
			if fmt.Sprint(b.version) == parts[2] {
				// The certificate as Google writes it: upper-case hex pairs separated by colons.
				var pairs []string
				for i := 0; i+2 <= len(b.signer); i += 2 {
					pairs = append(pairs, strings.ToUpper(b.signer[i:i+2]))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"generatedApks": []any{map[string]any{
					"certificateSha256Hash": strings.Join(pairs, ":"),
					"generatedSplitApks":    []any{map[string]any{"downloadId": "split/" + parts[2]}},
					"generatedUniversalApk": map[string]any{"downloadId": "uni/" + parts[2] + "=="},
				}}})
				return
			}
		}
		http.NotFound(w, r)
	case len(parts) == 5 && parts[1] == "generatedApks" && parts[3] == "downloads" && r.URL.Query().Get("alt") == "media":
		for _, b := range builds {
			if parts[4] == "uni/"+fmt.Sprint(b.version)+"==:download" {
				_, _ = w.Write(fixtureAPK(f.t, b.file))
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakePlay) validAssertion(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Scope string `json:"scope"`
		Iss   string `json:"iss"`
	}
	_ = json.Unmarshal(raw, &claims)
	return claims.Scope == playAndroidPublisher && claims.Iss == "reader@play-e2e.iam.gserviceaccount.com"
}

const (
	stateInReview  = "RELEASE_LIFECYCLE_STATE_IN_REVIEW"
	statePublished = "RELEASE_LIFECYCLE_STATE_PUBLISHED"
)

// waitFor polls until ok returns true or the deadline passes, and says what it was waiting for.
func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", within, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func managedVersion(p policyResponseDTO, pkg string) int64 {
	for _, a := range p.Desired.ManagedApps {
		if a.PackageName == pkg {
			return a.VersionCode
		}
	}
	return 0
}

// TestAFollowedPlayAppReachesEveryChildAndUpdatesItself is the whole of what a parent asked for:
// an app from Google Play, on every child's phone, updated without anyone doing anything when
// Google publishes a new version — and never a build Google has not finished reviewing.
func TestAFollowedPlayAppReachesEveryChildAndUpdatesItself(t *testing.T) {
	play := newFakePlay(t)
	// Version 1 is published; version 2 is still in Google's review.
	play.publish(fixturePackage, playBuild{"production", 1, statePublished, "fixture-v1.apk", fixtureSignerSHA256})
	play.publish(fixturePackage, playBuild{"production", 2, stateInReview, "fixture-v2.apk", fixtureSignerSHA256})

	h, _ := catalogHarness(t, play.options()...)
	f := enrolledFixture(t, h)

	// Before anything follows it, a package nobody has uploaded is not something to declare.
	h.call(http.MethodPut, "/family/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expectError(http.StatusBadRequest, "unknown_package")

	var src playSourceDTO
	h.call(http.MethodPut, "/play-sources/"+fixturePackage, f.parent.Token,
		map[string]any{"tracks": []string{"production"}, "label": "Wortspiel"}).expect(http.StatusOK).decode(&src)
	if src.PackageName != fixturePackage || len(src.Tracks) != 1 || src.Tracks[0] != "production" {
		t.Fatalf("the source is %+v", src)
	}
	h.call(http.MethodPut, "/family/managed-apps/"+fixturePackage, f.parent.Token, nil).expect(http.StatusNoContent)

	var sync1 playSyncDTO
	h.call(http.MethodPost, "/play-sources/sync", f.parent.Token, nil).expect(http.StatusOK).decode(&sync1)
	if len(sync1.Errors) != 0 {
		t.Fatalf("the sync reported errors: %v", sync1.Errors)
	}
	if play.tokens == 0 {
		t.Fatal("the server never exchanged its JWT for a token; the fake was not what it read")
	}

	// The published build, not the one in review, and recorded as coming from Play.
	pol := h.devicePolicy(f.deviceToken())
	if v := managedVersion(pol, fixturePackage); v != 1 {
		t.Fatalf("the phone is told version %d; want 1 — the published one, never the build in review: %+v", v, pol.Desired.ManagedApps)
	}
	var list appListDTO
	h.call(http.MethodGet, "/apps", f.parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Apps) != 1 || list.Apps[0].Source != "PLAY" || list.Apps[0].Label != "Wortspiel" ||
		list.Apps[0].SignerSHA256 != fixtureSignerSHA256 {
		t.Fatalf("the catalog holds %+v; want one PLAY build labelled Wortspiel signed by the fixture key", list.Apps)
	}

	// Google finishes its review: the next check fetches it, and the phone is told without any
	// parent action — a new policy version, and the new build in it.
	before := pol.Desired.PolicyVersion
	play.promote(fixturePackage, 2, statePublished)
	var sync2 playSyncDTO
	h.call(http.MethodPost, "/play-sources/sync", f.parent.Token, nil).expect(http.StatusOK).decode(&sync2)
	if len(sync2.Registered) != 1 || sync2.Registered[0].VersionCode != 2 {
		t.Fatalf("the second sync registered %+v; want version 2", sync2.Registered)
	}
	after := h.devicePolicy(f.deviceToken())
	if v := managedVersion(after, fixturePackage); v != 2 {
		t.Fatalf("after Google published version 2 the phone is told version %d", v)
	}
	if after.Desired.PolicyVersion <= before {
		t.Fatalf("policy version %d -> %d: the phone would not know to look again", before, after.Desired.PolicyVersion)
	}

	// A third check with nothing new fetches nothing.
	var sync3 playSyncDTO
	h.call(http.MethodPost, "/play-sources/sync", f.parent.Token, nil).expect(http.StatusOK).decode(&sync3)
	if len(sync3.Registered) != 0 || sync3.Checked != 1 {
		t.Fatalf("a check with nothing new registered %+v (checked %d)", sync3.Registered, sync3.Checked)
	}

	// The console sees the last check.
	var sources playSourcesDTO
	h.call(http.MethodGet, "/play-sources", f.parent.Token, nil).expect(http.StatusOK).decode(&sources)
	if !sources.Configured || sources.Account != "reader@play-e2e.iam.gserviceaccount.com" || len(sources.Sources) != 1 {
		t.Fatalf("the listing is %+v", sources)
	}
	s := sources.Sources[0]
	if s.CheckedAt == nil || s.PublishedVersion == nil || *s.PublishedVersion != 2 || s.LastError != "" {
		t.Fatalf("the source's last check reads %+v", s)
	}

	// FR-16.7: a child added after the declaration has the app too, with nothing done for it.
	later := h.newChild(f.parent.Token, "Noa")
	dev := h.newDevice(f.parent.Token, later.ID, "Noa's phone")
	_, enrollToken := h.provision(f.parent.Token, dev.ID)
	enrolled := h.enrollDevice(enrollToken, "Galaxy S20", "Android 13", []string{oemDialer})
	if v := managedVersion(h.devicePolicy(enrolled.DeviceToken), fixturePackage); v != 2 {
		t.Fatalf("a child added later is told version %d of the family's app; want 2", v)
	}
}

// TestTheFamilySetAndAChildsOwnAreSeparateDecisions. Withdrawing an app from every child removes
// it from every child that has no declaration of its own — and only from those.
func TestTheFamilySetAndAChildsOwnAreSeparateDecisions(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)
	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)

	other := h.newChild(f.parent.Token, "Noa")
	dev := h.newDevice(f.parent.Token, other.ID, "Noa's phone")
	_, enrollToken := h.provision(f.parent.Token, dev.ID)
	otherToken := h.enrollDevice(enrollToken, "Galaxy S20", "Android 13", []string{oemDialer}).DeviceToken

	h.call(http.MethodPut, "/family/managed-apps/"+fixturePackage, f.parent.Token, nil).expect(http.StatusNoContent)
	for name, token := range map[string]string{"Mira": f.deviceToken(), "Noa": otherToken} {
		if v := managedVersion(h.devicePolicy(token), fixturePackage); v != 1 {
			t.Fatalf("%s's phone is told version %d of the family's app; want 1", name, v)
		}
	}

	// The child view shows it, marked as the family's.
	var mine struct {
		ManagedApps []familyManagedAppDTO `json:"managed_apps"`
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/managed-apps", f.parent.Token, nil).expect(http.StatusOK).decode(&mine)
	if len(mine.ManagedApps) != 1 || !mine.ManagedApps[0].ForAllChildren || !mine.ManagedApps[0].Available {
		t.Fatalf("the child's list is %+v; want the family's app, marked for_all_children", mine.ManagedApps)
	}
	var fam struct {
		ManagedApps []familyManagedAppDTO `json:"managed_apps"`
	}
	h.call(http.MethodGet, "/family/managed-apps", f.parent.Token, nil).expect(http.StatusOK).decode(&fam)
	if len(fam.ManagedApps) != 1 || fam.ManagedApps[0].PackageName != fixturePackage {
		t.Fatalf("the family's list is %+v", fam.ManagedApps)
	}

	// Mira also has it on her own account; then the family withdraws it.
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).expect(http.StatusNoContent)
	h.call(http.MethodDelete, "/family/managed-apps/"+fixturePackage, f.parent.Token, nil).expect(http.StatusNoContent)

	if v := managedVersion(h.devicePolicy(f.deviceToken()), fixturePackage); v != 1 {
		t.Fatalf("Mira declared it herself, and the family withdrawal took it away (version %d)", v)
	}
	if v := managedVersion(h.devicePolicy(otherToken), fixturePackage); v != 0 {
		t.Fatalf("Noa had it only through the family, and still has version %d after the withdrawal", v)
	}
	h.call(http.MethodDelete, "/family/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expectError(http.StatusNotFound, "not_found")
}

// TestAPlayBuildThatIsNotWhatPlayNamedIsRefused. The signer pin protects every later version; the
// first one is protected by Play's own account of the certificate. A file that does not carry it
// is not kept, and the console says why.
func TestAPlayBuildThatIsNotWhatPlayNamedIsRefused(t *testing.T) {
	play := newFakePlay(t)
	// Play says "signed by the fixture key" and serves a file signed by another one.
	play.publish(fixturePackage, playBuild{"production", 1, statePublished, "fixture-othersigner.apk", fixtureSignerSHA256})
	h, _ := catalogHarness(t, play.options()...)
	parent := h.signIn(primaryParent)

	h.call(http.MethodPut, "/play-sources/"+fixturePackage, parent.Token, map[string]any{"tracks": []string{"production"}}).
		expect(http.StatusOK)
	var res playSyncDTO
	h.call(http.MethodPost, "/play-sources/sync", parent.Token, nil).expect(http.StatusOK).decode(&res)
	if len(res.Registered) != 0 || !strings.Contains(res.Errors[fixturePackage], "not the build its source named") {
		t.Fatalf("a file not signed as Play named was handled as %+v", res)
	}
	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Apps) != 0 {
		t.Fatalf("the refused file is in the catalog: %+v", list.Apps)
	}
	var sources playSourcesDTO
	h.call(http.MethodGet, "/play-sources", parent.Token, nil).expect(http.StatusOK).decode(&sources)
	if len(sources.Sources) != 1 || !strings.Contains(sources.Sources[0].LastError, otherFixtureSignerSHA256) {
		t.Fatalf("the console is not told which key the file carried: %+v", sources.Sources)
	}

	// And Play refusing access is named as the Play Console grant it is.
	play.mu.Lock()
	play.denied = true
	play.mu.Unlock()
	h.call(http.MethodPost, "/play-sources/sync", parent.Token, nil).expect(http.StatusOK).decode(&res)
	if !strings.Contains(res.Errors[fixturePackage], "Play Console") {
		t.Fatalf("a 403 from Play reads %q; it must send the parent to the Play Console", res.Errors[fixturePackage])
	}
}

// TestPlaySyncRunsByItself. Nobody presses "check now" in real life: the server looks on its own,
// and a release Google publishes reaches the phone with no request at all.
func TestPlaySyncRunsByItself(t *testing.T) {
	play := newFakePlay(t)
	play.publish(fixturePackage, playBuild{"alpha", 1, stateInReview, "fixture-v1.apk", fixtureSignerSHA256})
	h, _ := catalogHarness(t, append(play.options(), withEnv("PLAY_SYNC_INTERVAL", "1s"))...)
	f := enrolledFixture(t, h)

	h.call(http.MethodPut, "/play-sources/"+fixturePackage, f.parent.Token,
		map[string]any{"tracks": []string{"production", "alpha"}}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expect(http.StatusNoContent)

	// Nothing is published yet, so nothing may arrive — measured over several ticks of the loop.
	waitFor(t, 10*time.Second, "the loop to check the in-review release", func() bool {
		var s playSourcesDTO
		h.call(http.MethodGet, "/play-sources", f.parent.Token, nil).expect(http.StatusOK).decode(&s)
		return len(s.Sources) == 1 && s.Sources[0].CheckedAt != nil
	})
	time.Sleep(2500 * time.Millisecond)
	if v := managedVersion(h.devicePolicy(f.deviceToken()), fixturePackage); v != 0 {
		t.Fatalf("a release still in review reached the phone as version %d", v)
	}

	play.promote(fixturePackage, 1, statePublished)
	waitFor(t, 15*time.Second, "the loop to fetch the published release with no request", func() bool {
		return managedVersion(h.devicePolicy(f.deviceToken()), fixturePackage) == 1
	})
}

// TestPlayWithoutAccessSaysSo. A server with no Play credential still lets a parent follow an app
// — that is their decision — and says plainly that it cannot read Play yet.
func TestPlayWithoutAccessSaysSo(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	h.call(http.MethodPut, "/play-sources/"+fixturePackage, parent.Token, map[string]any{"tracks": []string{"production"}}).
		expect(http.StatusOK)
	var sources playSourcesDTO
	h.call(http.MethodGet, "/play-sources", parent.Token, nil).expect(http.StatusOK).decode(&sources)
	if sources.Configured || len(sources.Sources) != 1 {
		t.Fatalf("the listing is %+v; want configured=false with the one followed app", sources)
	}
	h.call(http.MethodPost, "/play-sources/sync", parent.Token, nil).expectError(http.StatusNotFound, "not_configured")

	h.call(http.MethodPut, "/play-sources/"+fixturePackage, parent.Token, map[string]any{"tracks": []string{"no such track!"}}).
		expectError(http.StatusBadRequest, "invalid_input")
	h.call(http.MethodDelete, "/play-sources/"+fixturePackage, parent.Token, nil).expect(http.StatusNoContent)
	h.call(http.MethodDelete, "/play-sources/"+fixturePackage, parent.Token, nil).expectError(http.StatusNotFound, "not_found")
}
