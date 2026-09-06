package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// APKDownloadPath is where the DPC is served from. It is referenced by the deployment's APK_URL,
// and named as a constant so the route and the URL cannot drift apart silently.
const APKDownloadPath = "/dpc.apk"

// serveAPK hands the provisioning downloader the DPC.
//
// **Unauthenticated, necessarily.** Android's setup wizard fetches this URL out of the QR payload
// on a factory-reset device that has no account, no session and no way to acquire one — the DPC in
// this response is what will later do the enrolling. There is nothing to authenticate with, and an
// endpoint that demanded a credential here would simply be a provisioning flow that cannot run.
//
// That is not a hole, because the APK is not the secret. It is the same signed artifact published
// on the release page; what binds this device to this family is the single-use enrollment token in
// the QR, and what binds the QR to these bytes is the SHA-256 the payload carries.
//
// **Serving from the same file the checksum was computed from is the whole point of this handler.**
// The alternative — APK_URL pointing at a release page or a bucket — makes the QR's checksum a
// claim about one artifact and the download a delivery of another, and the two drift the first time
// a build is republished. The device then refuses the download it just made, mid-provisioning, in
// front of a parent holding a wiped phone, with the reason visible only in a system log they cannot
// read.
//
// **So the correspondence is checked here, on every request, rather than assumed.** main.go hashes
// the file once at startup and that value goes into every QR this process issues; installing a new
// APK without restarting therefore leaves the server publishing a checksum for bytes it no longer
// has. That is the one failure in this system that cannot be diagnosed from the server side — the
// phone downloads, refuses, and says "Can't set up device" — and before this check it had no
// symptom here at all: the swap is a file copy on the node, not an event this process sees.
// Re-hashing costs ~25 ms against ~13 MB, which is small beside sending those same bytes, and the
// endpoint is reached once per enrolment. The file is hashed and served through ONE open
// descriptor, so no rewrite can slip in between the check and the response.
func (s *Server) serveAPK(c *gin.Context) {
	if s.cfg.APKPath == "" {
		// 404 rather than 500: this deployment does not host the DPC, which is a configuration a
		// server may legitimately have (the QR endpoint refuses for the same reason, and says so).
		failWith(c, http.StatusNotFound, "not_found", "this server does not host the DPC")
		return
	}
	f, err := os.Open(s.cfg.APKPath)
	if err != nil {
		// The path is checked at startup, so reaching here means the file was removed or replaced
		// while the server was running — which also means the checksum in every QR this server has
		// already issued describes bytes that are no longer here.
		s.log.Error("the configured APK is no longer readable",
			"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		s.log.Error("the configured APK is no longer readable",
			"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
		return
	}

	// An empty packageChecksum means nothing was computed to compare against — the QR endpoint
	// already refuses to issue a payload in that state, so there is no published claim to defend
	// and hashing here would prove nothing.
	if s.packageChecksum != "" {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			s.log.Error("the configured APK could not be read to the end",
				"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
			failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
			return
		}
		onDisk := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
		if onDisk != s.packageChecksum {
			// Both values are logged because the operator's next question is "which build is
			// this?", and neither is a secret: the right-hand one is printed in every QR.
			s.log.Error("the APK on disk is not the one the issued provisioning checksums describe; "+
				"the file was replaced without restarting this server",
				"path", s.cfg.APKPath, "on_disk", onDisk, "published", s.packageChecksum,
				"size", info.Size(), "request_id", RequestIDOf(c))
			failWith(c, http.StatusServiceUnavailable, "apk_changed",
				"the DPC on disk does not match the checksum this server publishes; it needs a restart")
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			s.log.Error("the configured APK could not be rewound after hashing",
				"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
			failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
			return
		}
	}

	// Set before ServeContent, which would otherwise sniff the first 512 bytes and call an APK a
	// ZIP. Android does not care about the type, but a parent who opens the link in a browser gets
	// a download rather than a garbled page.
	c.Header("Content-Type", "application/vnd.android.package-archive")
	c.Header("Content-Disposition", `attachment; filename="familyguard.apk"`)
	// ServeContent, not io.Copy: it answers Range requests, and the provisioning downloader
	// resumes a partial download rather than restarting it on a phone that walked out of Wi-Fi.
	// It takes the descriptor this handler already hashed rather than the path, which is what makes
	// the check above a statement about the bytes in this response and not about a file that
	// happened to be correct a moment earlier.
	http.ServeContent(c.Writer, c.Request, "familyguard.apk", info.ModTime(), f)
}

// hostedDPC answers what build of the DPC this deployment is serving.
//
// **The console cannot work this out for itself, and before this endpoint it did not try.** The
// "Update app" button was offered unconditionally, with a comment saying the server does not parse
// the APK it hosts and therefore does not know its version — which was true, and which meant a
// parent could not tell a phone that is up to date from one that is three builds behind. The phone
// could tell, but only after downloading 13 MB, and only when asked.
//
// Parent-authenticated rather than public: it names the package and version of the artifact the
// unauthenticated /dpc.apk serves, which is not a secret, but a deployment's version inventory is
// not something to volunteer to the internet either.
func (s *Server) hostedDPC(c *gin.Context) {
	if s.cfg.APKPath == "" || s.hostedAPK == nil {
		// 200 and "hosted": false, not 404. "This deployment hosts no DPC" is a configuration a
		// server may legitimately have, and the console draws it as a plain absence — a 404 would
		// make an ordinary state arrive as an error the page has to distinguish from a real one.
		c.JSON(http.StatusOK, gin.H{"hosted": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"hosted":       true,
		"package_name": s.hostedAPK.PackageName,
		"version_name": s.hostedAPK.VersionName,
		"version_code": s.hostedAPK.VersionCode,
		"min_sdk":      s.hostedAPK.MinSDK,
		"size":         s.hostedAPK.Size,
		// The same value every provisioning QR carries, and the same one the phone re-computes
		// after downloading. Published here so a parent comparing a release page with a running
		// deployment has one number to compare rather than a version string that can be reused.
		"package_checksum": s.packageChecksum,
	})
}
