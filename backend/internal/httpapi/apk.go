package httpapi

import (
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
// read. Here the two cannot disagree: main.go hashes cfg.APKPath at startup, and this reads it.
func (s *Server) serveAPK(c *gin.Context) {
	if s.cfg.APKPath == "" {
		// 404 rather than 500: this deployment does not host the DPC, which is a configuration a
		// server may legitimately have (the QR endpoint refuses for the same reason, and says so).
		failWith(c, http.StatusNotFound, "not_found", "this server does not host the DPC")
		return
	}
	info, err := os.Stat(s.cfg.APKPath)
	if err != nil || info.IsDir() {
		// The path is checked at startup, so reaching here means the file was removed or replaced
		// while the server was running — which also means the checksum in every QR this server has
		// already issued describes bytes that are no longer here.
		s.log.Error("the configured APK is no longer readable",
			"path", s.cfg.APKPath, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "the DPC is temporarily unavailable")
		return
	}

	// Set before ServeFile, which would otherwise sniff the first 512 bytes and call an APK a ZIP.
	// Android does not care about the type, but a parent who opens the link in a browser gets a
	// download rather than a garbled page.
	c.Header("Content-Type", "application/vnd.android.package-archive")
	c.Header("Content-Disposition", `attachment; filename="familyguard.apk"`)
	// http.ServeFile, not io.Copy: it answers Range requests, and the provisioning downloader
	// resumes a partial download rather than restarting it on a phone that walked out of Wi-Fi.
	http.ServeFile(c.Writer, c.Request, s.cfg.APKPath)
}
