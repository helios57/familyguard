// Package play reads Google Play's own record of an application and fetches the build Google
// published (FR-16.8).
//
// Two reads, both edit-free and both on the Play Developer API:
//
//   - tracks/{track}/releases names each release on a track with its lifecycle state, so the server
//     can tell a release Google has finished checking (PUBLISHED) from one still in review;
//   - generatedApks/{versionCode} names the APKs Google built from the uploaded bundle, among them a
//     universal APK signed with the app's Play signing key — the same bytes Play hands a phone that
//     cannot take splits. That file is what the server downloads. It never builds or re-signs one.
//
// The credential is a service account the app's developer has invited into the Play Console. It
// needs read access to the apps it follows and nothing else; a publisher's key would work and is
// the wrong thing to hand a server whose job is only to read.
package play

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const scope = "https://www.googleapis.com/auth/androidpublisher"

// PublishedState is the one lifecycle state whose build the server takes. A release in review,
// drafted or halted is one Google has not finished with or has pulled.
const PublishedState = "RELEASE_LIFECYCLE_STATE_PUBLISHED"

// Client reads the Play Developer API as one service account.
type Client struct {
	endpoint string
	email    string
	client   *http.Client
	download *http.Client
}

// New builds a client from a service-account key. endpoint is https://androidpublisher.googleapis.com
// in production and a local server in tests; the token endpoint comes from the key itself.
func New(ctx context.Context, credentials []byte, endpoint string) (*Client, error) {
	creds, err := google.CredentialsFromJSONWithType(ctx, credentials, google.ServiceAccount, scope)
	if err != nil {
		return nil, fmt.Errorf("play: the credentials are not a service-account key: %w", err)
	}
	var who struct {
		ClientEmail string `json:"client_email"`
	}
	_ = json.Unmarshal(credentials, &who)
	api := oauth2.NewClient(ctx, creds.TokenSource)
	api.Timeout = 20 * time.Second
	// A universal APK is megabytes; its read is bounded by the caller's context and size limit,
	// not by a timeout sized for a JSON answer.
	dl := oauth2.NewClient(ctx, creds.TokenSource)
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		email:    who.ClientEmail,
		client:   api,
		download: dl,
	}, nil
}

// Account is the service account's address — what a developer invites in the Play Console.
func (c *Client) Account() string { return c.email }

// Release is one release on one track.
type Release struct {
	Track       string
	Name        string
	State       string
	VersionCode int64
}

// LatestPublished returns the release with the highest version code that is PUBLISHED on any of
// the tracks, or nil when none of them has one. A release with several artifacts counts as its
// highest; a phone installs one APK, and the highest is what Play would serve it.
func (c *Client) LatestPublished(ctx context.Context, pkg string, tracks []string) (*Release, error) {
	var best *Release
	for _, track := range tracks {
		releases, err := c.Releases(ctx, pkg, track)
		if err != nil {
			return nil, err
		}
		for i := range releases {
			r := releases[i]
			if r.State != PublishedState {
				continue
			}
			if best == nil || r.VersionCode > best.VersionCode {
				best = &r
			}
		}
	}
	return best, nil
}

// Releases lists the releases on one track.
func (c *Client) Releases(ctx context.Context, pkg, track string) ([]Release, error) {
	var out struct {
		Releases []struct {
			ReleaseName     string `json:"releaseName"`
			State           string `json:"releaseLifecycleState"`
			ActiveArtifacts []struct {
				VersionCode versionCode `json:"versionCode"`
			} `json:"activeArtifacts"`
		} `json:"releases"`
	}
	if err := c.get(ctx, c.appURL(pkg, "tracks", track, "releases"), &out); err != nil {
		return nil, fmt.Errorf("play: releases of %s on %s: %w", pkg, track, err)
	}
	releases := []Release{}
	for _, r := range out.Releases {
		var highest int64
		for _, a := range r.ActiveArtifacts {
			highest = max(highest, int64(a.VersionCode))
		}
		if highest == 0 {
			continue
		}
		releases = append(releases, Release{Track: track, Name: r.ReleaseName, State: r.State, VersionCode: highest})
	}
	return releases, nil
}

// UniversalAPK is the universal APK Google generated for one version code.
type UniversalAPK struct {
	// SignerSHA256 is the certificate Play says it signed the APK with, lower-case hex. The caller
	// checks the downloaded file against it before keeping it.
	SignerSHA256 string
	DownloadID   string
}

// ErrNoUniversalAPK means Google generated no universal APK for the version — a bundle whose
// configuration excludes one. There is then no single file a phone could be given.
var ErrNoUniversalAPK = errors.New("play: Google generated no universal APK for this version")

// Universal names the universal APK of one version.
func (c *Client) Universal(ctx context.Context, pkg string, vc int64) (*UniversalAPK, error) {
	var out struct {
		GeneratedApks []struct {
			CertificateSha256Hash string `json:"certificateSha256Hash"`
			GeneratedUniversalApk *struct {
				DownloadID string `json:"downloadId"`
			} `json:"generatedUniversalApk"`
		} `json:"generatedApks"`
	}
	if err := c.get(ctx, c.appURL(pkg, "generatedApks", strconv.FormatInt(vc, 10)), &out); err != nil {
		return nil, fmt.Errorf("play: generated APKs of %s %d: %w", pkg, vc, err)
	}
	for _, g := range out.GeneratedApks {
		if g.GeneratedUniversalApk != nil && g.GeneratedUniversalApk.DownloadID != "" {
			return &UniversalAPK{
				SignerSHA256: strings.ToLower(strings.ReplaceAll(g.CertificateSha256Hash, ":", "")),
				DownloadID:   g.GeneratedUniversalApk.DownloadID,
			}, nil
		}
	}
	return nil, fmt.Errorf("%w (%s %d)", ErrNoUniversalAPK, pkg, vc)
}

// Download opens the universal APK's bytes. The caller closes the body.
func (c *Client) Download(ctx context.Context, pkg string, vc int64, downloadID string) (io.ReadCloser, error) {
	u := c.appURL(pkg, "generatedApks", strconv.FormatInt(vc, 10), "downloads", downloadID+":download") + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.download.Do(req)
	if err != nil {
		return nil, fmt.Errorf("play: download %s %d: %w", pkg, vc, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("play: download %s %d: %w", pkg, vc, answerError(resp))
	}
	return resp.Body, nil
}

func (c *Client) appURL(pkg string, parts ...string) string {
	var b strings.Builder
	b.WriteString(c.endpoint)
	b.WriteString("/androidpublisher/v3/applications/")
	b.WriteString(url.PathEscape(pkg))
	for _, p := range parts {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(p))
	}
	return b.String()
}

func (c *Client) get(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into)
}

// answerError turns a Google error answer into a sentence a parent can act on. The two that
// matter are named: 401/403 is the service account lacking access to this app in the Play
// Console, and 404 is a package Play does not know or a track it does not have.
func answerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var g struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &g)
	msg := g.Error.Message
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Google Play refused access (%d %s): the service account needs read access to this app in the Play Console: %s",
			resp.StatusCode, g.Error.Status, msg)
	case http.StatusNotFound:
		return fmt.Errorf("Google Play does not know it (404): %s", msg)
	}
	return fmt.Errorf("Google Play answered %d %s: %s", resp.StatusCode, g.Error.Status, msg)
}

// versionCode accepts a version code as a JSON number or as a string: the API documents int64
// fields as strings, and measured answers carry numbers.
type versionCode int64

func (v *versionCode) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("version code %s: %w", b, err)
	}
	*v = versionCode(n)
	return nil
}
