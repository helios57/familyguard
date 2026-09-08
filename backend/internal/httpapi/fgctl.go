package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// FgctlManifestPath is where a client asks what this deployment hosts.
//
// It and the download below are OUTSIDE every auth group, like the DPC at /dpc.apk and for the same
// reason: the whole point is to be reachable by someone who does not yet have a credential. There
// is nothing to protect. The binary contains no secret -- it is the same file published for every
// deployment -- and the manifest describes only bytes that are already downloadable, so requiring a
// key for the manifest while leaving the binary open would protect nothing and would break the one
// case that matters most: `fgctl self-update` recovering a machine whose stored key has been
// revoked, which is exactly when you need a working binary.
const (
	FgctlManifestPath = "/fgctl"
	FgctlDownloadPath = "/fgctl/:name"
)

// fgctlManifest answers "what is the current fgctl, and what should it hash to".
func (s *Server) fgctlManifest(c *gin.Context) {
	if s.fgctl == nil || len(s.fgctl.Artifacts) == 0 {
		// 200 and "hosted": false, not 404 -- a deployment that ships no CLI is a configuration a
		// server may legitimately have, and the console draws it as a plain absence. A 404 would
		// arrive as an error the page has to tell apart from a real one.
		c.JSON(http.StatusOK, gin.H{"hosted": false})
		return
	}
	type artifact struct {
		Name   string `json:"name"`
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
		URL    string `json:"url"`
	}
	out := make([]artifact, 0, len(s.fgctl.Artifacts))
	for _, a := range s.fgctl.Artifacts {
		out = append(out, artifact{
			Name: a.Name, OS: a.OS, Arch: a.Arch, Size: a.Size, SHA256: a.SHA256,
			// A path, not an absolute URL. The server does not reliably know its own external
			// origin behind an ingress, and a client that reached this endpoint already knows the
			// base it used -- so joining is its job, and it cannot be sent somewhere else by a
			// misconfigured PUBLIC_URL.
			URL: "/fgctl/" + a.Name,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"hosted": true,
		// The server's own version. These binaries are compiled in the same image build from the
		// same source, so this is one number rather than two that can disagree.
		"version":   s.fgctl.Version,
		"artifacts": out,
	})
}

// serveFgctl hands over one binary.
func (s *Server) serveFgctl(c *gin.Context) {
	if s.fgctl == nil {
		failWith(c, http.StatusNotFound, "not_found", "this server does not host fgctl")
		return
	}
	// Find, not a path join on user input: the name has to be one this catalog produced, so a
	// request cannot describe a path at all and traversal is not something to defend against
	// downstream.
	artifact, ok := s.fgctl.Find(c.Param("name"))
	if !ok {
		failWith(c, http.StatusNotFound, "not_found", "no such fgctl build")
		return
	}
	path := s.fgctl.Path(artifact)
	f, err := os.Open(path)
	if err != nil {
		s.log.Error("a catalogued fgctl binary is no longer readable",
			"path", path, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "fgctl_unavailable", "that build is temporarily unavailable")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		s.log.Error("a catalogued fgctl binary is no longer readable",
			"path", path, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "fgctl_unavailable", "that build is temporarily unavailable")
		return
	}

	// Re-hashed per request, and compared with what the manifest publishes. The manifest is a
	// claim about specific bytes and clients are told to verify against it; serving different bytes
	// under that claim would make every client's check fail with no way to tell a corrupted
	// download from a changed file. The cost is one hash of a ~9 MB file on a route used a handful
	// of times per release.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		s.log.Error("a catalogued fgctl binary could not be read to the end",
			"path", path, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "fgctl_unavailable", "that build is temporarily unavailable")
		return
	}
	if onDisk := hex.EncodeToString(h.Sum(nil)); onDisk != artifact.SHA256 {
		s.log.Error("the fgctl binary on disk is not the one this server publishes a checksum for; "+
			"the file was replaced without restarting this server",
			"path", path, "on_disk", onDisk, "published", artifact.SHA256, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "fgctl_changed",
			"that build does not match the checksum this server publishes; it needs a restart")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.log.Error("a catalogued fgctl binary could not be rewound after hashing",
			"path", path, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "fgctl_unavailable", "that build is temporarily unavailable")
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="`+artifact.Name+`"`)
	// The checksum travels with the response as well as in the manifest, so a download made with
	// curl can be checked without a second request.
	c.Header("X-Fgctl-SHA256", artifact.SHA256)
	c.Header("X-Fgctl-Version", s.fgctl.Version)
	// ServeContent rather than io.Copy: it answers Range requests, so an interrupted download
	// resumes. It takes the descriptor already hashed above rather than the path, which is what
	// makes that check a statement about the bytes in THIS response.
	http.ServeContent(c.Writer, c.Request, artifact.Name, info.ModTime(), f)
}
