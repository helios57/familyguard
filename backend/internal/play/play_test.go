package play

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakePlay is the two Google endpoints the client touches: OAuth, which verifies the service
// account's JWT signature with the key's public half, and the Play Developer API. Real HTTP and
// real signing; only the far end is ours.
type fakePlay struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	mu       sync.Mutex
	releases map[string]string // "pkg/track" -> JSON body
	apks     map[string]string // "pkg/vc" -> JSON body
	files    map[string][]byte // downloadId -> bytes
	status   int               // when non-zero, every API call answers this
	auth     []string
}

func newFakePlay(t *testing.T) *fakePlay {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePlay{key: key, releases: map[string]string{}, apks: map[string]string{}, files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePlay) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		if !f.validAssertion(r.Form.Get("assertion")) {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-play","token_type":"Bearer","expires_in":3600}`)
		return
	}
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"The caller does not have permission"}}`)
		return
	}
	const prefix = "/androidpublisher/v3/applications/"
	// Split the ESCAPED path: a download id may carry a '/', which the client escapes, and the
	// decoded path would split it into two segments.
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	for i := range parts {
		parts[i], _ = url.PathUnescape(parts[i])
	}
	switch {
	case len(parts) == 4 && parts[1] == "tracks" && parts[3] == "releases":
		body, ok := f.releases[parts[0]+"/"+parts[2]]
		if !ok {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"Track not found"}}`, 404)
			return
		}
		_, _ = io.WriteString(w, body)
	case len(parts) == 3 && parts[1] == "generatedApks":
		body, ok := f.apks[parts[0]+"/"+parts[2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	case len(parts) == 5 && parts[1] == "generatedApks" && parts[3] == "downloads":
		id, _ := strings.CutSuffix(parts[4], ":download")
		b, ok := f.files[id]
		if !ok || r.URL.Query().Get("alt") != "media" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
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
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(claims, &c)
	return c.Scope == scope
}

func (f *fakePlay) client(t *testing.T) *Client {
	der, _ := x509.MarshalPKCS8PrivateKey(f.key)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "play-test", "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "reader@play-test.iam.gserviceaccount.com", "client_id": "1",
		"token_uri": f.srv.URL + "/token",
	})
	c, err := New(context.Background(), sa, f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLatestPublishedTakesTheHighestPublishedReleaseAcrossTracks(t *testing.T) {
	f := newFakePlay(t)
	// Production holds a newer build still in review; the testing track holds the newest
	// PUBLISHED one, as a number on one release and as a string on another (both forms occur).
	f.releases["com.example.game/production"] = `{"releases":[
		{"releaseName":"9","activeArtifacts":[{"versionCode":9}],"releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_IN_REVIEW"},
		{"releaseName":"3","activeArtifacts":[{"versionCode":"3"}],"releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_PUBLISHED"}]}`
	f.releases["com.example.game/alpha"] = `{"releases":[
		{"releaseName":"7","activeArtifacts":[{"versionCode":7}],"releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_PUBLISHED"},
		{"releaseName":"8","activeArtifacts":[{"versionCode":8}],"releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_DRAFT"}]}`

	got, err := f.client(t).LatestPublished(context.Background(), "com.example.game", []string{"production", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.VersionCode != 7 || got.Track != "alpha" {
		t.Fatalf("want version 7 from alpha (the highest PUBLISHED), got %+v", got)
	}
	if len(f.auth) == 0 || f.auth[0] != "Bearer at-play" {
		t.Fatalf("the API was not called with the exchanged token: %v", f.auth)
	}
}

func TestLatestPublishedIsNilWhenNothingIsPublished(t *testing.T) {
	f := newFakePlay(t)
	f.releases["com.example.new/production"] = `{"releases":[
		{"releaseName":"1","activeArtifacts":[{"versionCode":1}],"releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_IN_REVIEW"}]}`
	f.releases["com.example.new/alpha"] = `{}`
	got, err := f.client(t).LatestPublished(context.Background(), "com.example.new", []string{"production", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("nothing is published, got %+v", got)
	}
}

func TestUniversalAndDownload(t *testing.T) {
	f := newFakePlay(t)
	f.apks["com.example.game/7"] = `{"generatedApks":[{"certificateSha256Hash":"AB:CD:EF",
		"generatedSplitApks":[{"downloadId":"split-1"}],
		"generatedUniversalApk":{"downloadId":"uni/7=="}}]}`
	f.files["uni/7=="] = []byte("PK-the-apk-bytes")
	c := f.client(t)

	u, err := c.Universal(context.Background(), "com.example.game", 7)
	if err != nil {
		t.Fatal(err)
	}
	if u.SignerSHA256 != "abcdef" || u.DownloadID != "uni/7==" {
		t.Fatalf("got %+v", u)
	}
	body, err := c.Download(context.Background(), "com.example.game", 7, u.DownloadID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	b, _ := io.ReadAll(body)
	if string(b) != "PK-the-apk-bytes" {
		t.Fatalf("downloaded %q", b)
	}
}

func TestUniversalAbsentIsNamed(t *testing.T) {
	f := newFakePlay(t)
	f.apks["com.example.game/7"] = `{"generatedApks":[{"certificateSha256Hash":"ab","generatedSplitApks":[{"downloadId":"s"}]}]}`
	_, err := f.client(t).Universal(context.Background(), "com.example.game", 7)
	if !errors.Is(err, ErrNoUniversalAPK) {
		t.Fatalf("want ErrNoUniversalAPK, got %v", err)
	}
}

func TestRefusedAccessSaysWhatToDo(t *testing.T) {
	f := newFakePlay(t)
	f.status = http.StatusForbidden
	_, err := f.client(t).LatestPublished(context.Background(), "com.example.game", []string{"production"})
	if err == nil || !strings.Contains(err.Error(), "Play Console") {
		t.Fatalf("a 403 must tell the parent to grant access in the Play Console, got %v", err)
	}
}
