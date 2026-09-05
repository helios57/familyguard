// Package config parses and validates the control plane's runtime configuration.
//
// Every value is read from the environment exactly once, at startup, and validated before the
// server binds a port. A configuration that would produce an insecure server is a startup failure,
// never a warning: there is no path where a missing signing key degrades into an open server.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/helios57/familyguard/backend/internal/provisioning"
)

// MinSessionKeyBytes is the smallest accepted HMAC signing key. 32 bytes matches the SHA-256
// output size used by HS256; anything shorter weakens the MAC.
const MinSessionKeyBytes = 32

// Default OIDC endpoints. They are configurable so the end-to-end suite can drive the real
// verification code against an issuer it controls, rather than mocking the verifier away.
const (
	DefaultOIDCIssuer    = "https://accounts.google.com"
	DefaultOIDCJWKSURL   = "https://www.googleapis.com/oauth2/v3/certs"
	DefaultOAuthAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultOAuthTokenURL = "https://oauth2.googleapis.com/token"
)

// Config is the fully validated configuration. Construct it only via Load.
type Config struct {
	Addr        string
	PublicURL   *url.URL
	DatabaseURL string

	OIDCIssuer    string
	OIDCJWKSURL   string
	OAuthClientID string
	// OAuthClientSecret enables the browser sign-in flow, in which this server performs the
	// authorization-code exchange itself. Without it the console cannot sign anyone in: the only
	// remaining way to a session is POST /auth/google with an ID token obtained elsewhere.
	OAuthClientSecret string
	OAuthAuthURL      string
	OAuthTokenURL     string
	SessionKey        []byte
	SessionTTL        time.Duration
	BootstrapEmail    []string

	AllowedOrigins []string

	// TrustedProxies lists the CIDRs whose X-Forwarded-For header may be believed. It is empty by
	// default: with no trusted proxy, the client address is the socket peer, and a forged header
	// cannot be used to escape the per-client rate limit or to poison an audit entry. Behind the
	// cluster ingress this must be set to the ingress controller's pod CIDR.
	TrustedProxies []string

	// APKURL is where a provisioning device downloads the DPC from. It must be absolute and
	// https, because Android verifies the download against the checksum we publish and will not
	// fetch cleartext during provisioning.
	APKURL *url.URL
	// APKPath, when set, is a local file whose SHA-256 is published in the QR payload. The
	// checksum is always computed from real bytes; it is never a configured constant.
	APKPath string
	// APKCertPath is the DER-encoded signing certificate of that APK. Its SHA-256 is the checksum
	// Android 7 and newer actually verifies, and it survives a rebuild of the same source — which
	// is why it is preferred over the APK's own hash rather than an alternative to it.
	APKCertPath string

	// APKDir is the managed-app catalog's one storage location (FR-16.1).
	//
	// All three ways an APK gets into the catalog put the file HERE: a scan of this directory at
	// startup, an upload from the console, and the same upload endpoint driven by an API key. One
	// location rather than one per route, because the alternative is a catalog row whose file lives
	// somewhere the other two routes cannot see — and the symptom of that is a phone that downloads
	// nothing, reported as the phone's fault.
	//
	// Distinct from APKPath, which is the DPC's own file and is served by a different, deliberately
	// unauthenticated route. The DPC is not a catalog entry: it is installed by the provisioning
	// wizard before any of this exists.
	APKDir string

	// FamilyName names the single family this deployment serves. It is cosmetic — every
	// authorization decision is made against the parents table — but it is what the console shows.
	FamilyName string

	DPCComponent string

	RateLimitPerMinute int
	MaxBodyBytes       int64
	// MaxUploadBytes applies to the one endpoint that receives a file (FR-16.2). It is separate
	// from MaxBodyBytes rather than a raise of it: the general cap exists so an unauthenticated
	// request is cheap to refuse, and an APK is three orders of magnitude larger than any JSON this
	// API accepts. Raising the general one to fit an APK would make every endpoint an easy way to
	// make this server read 256 MB.
	MaxUploadBytes     int64
	CommandTTL         time.Duration
	DeviceOfflineAfter time.Duration

	// Retention. A child's location history is the most sensitive thing this server holds, so it
	// expires by default and the window is short; the audit log is the record of what the adults
	// did and is kept for a year. Both are configurable because a family, not this code, decides.
	AuditRetentionDays  int
	LocationRetention   time.Duration
	MaintenanceInterval time.Duration

	LogLevel string
}

// Load reads and validates configuration from the environment. It returns every problem it finds,
// not just the first, so a misconfigured deployment is fixed in one pass.
func Load() (*Config, error) {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	c := &Config{
		Addr:                envOr("ADDR", ":8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		OIDCIssuer:          envOr("OIDC_ISSUER", DefaultOIDCIssuer),
		OIDCJWKSURL:         envOr("OIDC_JWKS_URL", DefaultOIDCJWKSURL),
		OAuthClientID:       os.Getenv("OAUTH_CLIENT_ID"),
		OAuthClientSecret:   os.Getenv("OAUTH_CLIENT_SECRET"),
		OAuthAuthURL:        envOr("OAUTH_AUTH_URL", DefaultOAuthAuthURL),
		OAuthTokenURL:       envOr("OAUTH_TOKEN_URL", DefaultOAuthTokenURL),
		APKPath:             os.Getenv("APK_PATH"),
		APKCertPath:         os.Getenv("APK_CERT_PATH"),
		APKDir:              os.Getenv("APK_DIR"),
		FamilyName:          envOr("FAMILY_NAME", "Family"),
		DPCComponent:        envOr("DPC_COMPONENT", "io.github.helios57.familyguard/.admin.AdminReceiver"),
		LogLevel:            envOr("LOG_LEVEL", "info"),
		SessionTTL:          24 * time.Hour,
		CommandTTL:          24 * time.Hour,
		DeviceOfflineAfter:  3 * time.Minute,
		MaintenanceInterval: 15 * time.Minute,
	}

	var err error

	if c.AuditRetentionDays, err = envInt("AUDIT_RETENTION_DAYS", 365); err != nil {
		fail("AUDIT_RETENTION_DAYS: %v", err)
	} else if c.AuditRetentionDays < 1 {
		fail("AUDIT_RETENTION_DAYS must be positive, got %d", c.AuditRetentionDays)
	}
	if locDays, err := envInt("LOCATION_RETENTION_DAYS", 30); err != nil {
		fail("LOCATION_RETENTION_DAYS: %v", err)
	} else if locDays < 1 {
		fail("LOCATION_RETENTION_DAYS must be positive, got %d", locDays)
	} else {
		c.LocationRetention = time.Duration(locDays) * 24 * time.Hour
	}

	if c.DatabaseURL == "" {
		fail("DATABASE_URL is required")
	}

	if c.OAuthClientID == "" {
		fail("OAUTH_CLIENT_ID is required (the audience every parent ID token must carry)")
	}
	if c.OAuthClientSecret == "" {
		// Refused rather than defaulted to "browser sign-in off". A server whose console shows a
		// sign-in button that can never work is a deployment that looks finished and is not.
		fail("OAUTH_CLIENT_SECRET is required; without it the console cannot complete a sign-in")
	}
	for _, ep := range []struct{ name, val string }{
		{"OAUTH_AUTH_URL", c.OAuthAuthURL},
		{"OAUTH_TOKEN_URL", c.OAuthTokenURL},
	} {
		if _, err := parseAbsURL(ep.val); err != nil {
			fail("%s: %v", ep.name, err)
		}
	}

	key := os.Getenv("SESSION_SIGNING_KEY")
	switch {
	case key == "":
		fail("SESSION_SIGNING_KEY is required")
	case len(key) < MinSessionKeyBytes:
		fail("SESSION_SIGNING_KEY must be at least %d bytes, got %d", MinSessionKeyBytes, len(key))
	default:
		c.SessionKey = []byte(key)
	}

	pub := os.Getenv("PUBLIC_URL")
	if pub == "" {
		fail("PUBLIC_URL is required (the externally reachable base URL of this server)")
	} else if u, err := parseAbsURL(pub); err != nil {
		fail("PUBLIC_URL: %v", err)
	} else if err := provisioning.RequireProvisioningURL(pub); err != nil {
		// PUBLIC_URL becomes server_url inside the provisioning payload, so the same rule applies
		// to it. Checked here rather than at the first QR request: a cleartext PUBLIC_URL otherwise
		// starts a server that serves every page correctly and refuses only the one action a parent
		// takes while standing over a factory-reset phone.
		fail("PUBLIC_URL: %v", err)
	} else {
		c.PublicURL = u
	}

	if apk := os.Getenv("APK_URL"); apk != "" {
		u, err := parseAbsURL(apk)
		// Android's provisioning downloader refuses cleartext, so an http:// value here produces a
		// QR code that always fails on a real device. The rule is provisioning's own, not a second
		// copy of it: this used to reject every http URL, which is stricter than what Payload
		// accepts, and the difference silently made one supported deployment shape unreachable.
		if err == nil {
			err = provisioning.RequireProvisioningURL(apk)
		}
		if err != nil {
			fail("APK_URL: %v", err)
		} else {
			c.APKURL = u
		}
		if c.APKPath == "" && c.APKCertPath == "" {
			// Without one of these there is no checksum, and provisioning.Payload refuses to build a
			// QR at all — so the parent taps "Setup QR" and gets a 500. Caught here instead, where
			// the message names the variable that is missing.
			fail("APK_URL is set but neither APK_PATH nor APK_CERT_PATH is: " +
				"the QR would carry no checksum and provisioning would refuse to build it")
		}
	}
	for _, p := range []struct{ name, val string }{
		{"APK_PATH", c.APKPath},
		{"APK_CERT_PATH", c.APKCertPath},
	} {
		if p.val == "" {
			continue
		}
		// Checked at startup rather than at the first QR request: a path typo that only surfaces
		// when a parent is standing over a factory-reset phone is the worst possible time to find it.
		if _, err := os.Stat(p.val); err != nil {
			fail("%s: %v", p.name, err)
		}
	}
	if c.APKDir != "" {
		// It must be a DIRECTORY, and it must be writable, and both are checked here rather than at
		// the first upload. A read-only mount is the likely mistake — the DPC's own APK is mounted
		// read-only today, and copying that stanza is the obvious way to configure this one — and
		// its symptom without this check is a parent uploading a 30 MB file over a phone connection
		// and being told "internal error" at the end of it.
		info, err := os.Stat(c.APKDir)
		switch {
		case err != nil:
			fail("APK_DIR: %v", err)
		case !info.IsDir():
			fail("APK_DIR: %s is not a directory", c.APKDir)
		default:
			probe, err := os.CreateTemp(c.APKDir, ".writable-*")
			if err != nil {
				fail("APK_DIR: %s is not writable: %v", c.APKDir, err)
			} else {
				name := probe.Name()
				_ = probe.Close()
				_ = os.Remove(name)
			}
		}
	}

	if u, err := parseIssuer(c.OIDCIssuer); err != nil {
		fail("OIDC_ISSUER: %v", err)
	} else {
		c.OIDCIssuer = u
	}
	if _, err := parseAbsURL(c.OIDCJWKSURL); err != nil {
		fail("OIDC_JWKS_URL: %v", err)
	}

	c.BootstrapEmail = splitList(os.Getenv("BOOTSTRAP_PARENT_EMAILS"))
	for _, e := range c.BootstrapEmail {
		if !strings.Contains(e, "@") {
			fail("BOOTSTRAP_PARENT_EMAILS contains %q, which is not an email address", e)
		}
	}

	// An empty origin list means "same-origin only", which is correct when the console is served
	// by this binary. A literal "*" is rejected: it cannot be combined with credentials, and the
	// browser silently drops the response rather than telling anyone why.
	c.AllowedOrigins = splitList(os.Getenv("ALLOWED_ORIGINS"))
	for _, o := range c.AllowedOrigins {
		if o == "*" {
			fail(`ALLOWED_ORIGINS must not contain "*"; list exact origins`)
			continue
		}
		if u, err := parseAbsURL(o); err != nil {
			fail("ALLOWED_ORIGINS entry %q: %v", o, err)
		} else if u.Path != "" && u.Path != "/" {
			fail("ALLOWED_ORIGINS entry %q must be scheme://host[:port] with no path", o)
		}
	}

	c.TrustedProxies = splitList(os.Getenv("TRUSTED_PROXIES"))
	for _, p := range c.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil {
			if net.ParseIP(p) == nil {
				fail("TRUSTED_PROXIES entry %q is neither an IP nor a CIDR", p)
			}
		}
	}

	if c.RateLimitPerMinute, err = envInt("RATE_LIMIT_PER_MINUTE", 120); err != nil {
		fail("RATE_LIMIT_PER_MINUTE: %v", err)
	} else if c.RateLimitPerMinute < 1 {
		fail("RATE_LIMIT_PER_MINUTE must be positive, got %d", c.RateLimitPerMinute)
	}

	maxBody, err := envInt("MAX_BODY_BYTES", 1<<20)
	if err != nil {
		fail("MAX_BODY_BYTES: %v", err)
	} else if maxBody < 1024 {
		fail("MAX_BODY_BYTES must be at least 1024, got %d", maxBody)
	} else {
		c.MaxBodyBytes = int64(maxBody)
	}

	// 256 MB. The DPC is 16 MB and the largest thing anyone would sensibly manage this way is a
	// game; the ceiling is what apk.MaxSize will read, and a value above it would be a cap the
	// parser then rejects a second time with a worse message.
	maxUpload, err := envInt("MAX_UPLOAD_BYTES", 256<<20)
	if err != nil {
		fail("MAX_UPLOAD_BYTES: %v", err)
	} else if int64(maxUpload) < c.MaxBodyBytes {
		fail("MAX_UPLOAD_BYTES (%d) is below MAX_BODY_BYTES (%d), which would make the upload "+
			"endpoint stricter than every other endpoint", maxUpload, c.MaxBodyBytes)
	} else {
		c.MaxUploadBytes = int64(maxUpload)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return c, nil
}

func parseAbsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("must be an absolute URL including scheme and host")
	}
	return u, nil
}

// parseIssuer normalises an OIDC issuer. Google is reached as both "accounts.google.com" and
// "https://accounts.google.com"; comparing the raw string would reject half of all valid tokens.
func parseIssuer(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("must not be empty")
	}
	// The issuer is compared byte-for-byte against every token's iss claim. A value containing
	// whitespace can never match one, so it would reject every sign-in while looking configured.
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return "", errors.New("must not contain whitespace")
	}
	return strings.TrimSuffix(trimmed, "/"), nil
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", v)
	}
	return n, nil
}

// DPCPackage is the package name half of DPC_COMPONENT.
//
// The component is "<package>/<class>" and is validated at startup, so the split cannot fail here;
// an unexpected value yields "" and the caller treats that as "no package is reserved" rather than
// reserving the empty string, which would reject every APK.
func (c *Config) DPCPackage() string {
	pkg, _, ok := strings.Cut(c.DPCComponent, "/")
	if !ok {
		return ""
	}
	return pkg
}
