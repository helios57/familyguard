package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validEnv is the smallest environment that must load. Every negative case below starts from it and
// breaks exactly one thing, so a failure names the field that caused it.
func validEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	base := map[string]string{
		"DATABASE_URL":            "postgres://user:pass@localhost:5432/familyguard",
		"OAUTH_CLIENT_ID":         "123456789.apps.googleusercontent.com",
		"OAUTH_CLIENT_SECRET":     "a-client-secret",
		"OAUTH_AUTH_URL":          "",
		"OAUTH_TOKEN_URL":         "",
		"SESSION_SIGNING_KEY":     strings.Repeat("k", 48),
		"PUBLIC_URL":              "https://guard.example.ch",
		"APK_URL":                 "",
		"APK_PATH":                "",
		"APK_CERT_PATH":           "",
		"ALLOWED_ORIGINS":         "",
		"BOOTSTRAP_PARENT_EMAILS": "",
		"OIDC_ISSUER":             "",
		"OIDC_JWKS_URL":           "",
		"RATE_LIMIT_PER_MINUTE":   "",
		"MAX_BODY_BYTES":          "",
		"AUDIT_RETENTION_DAYS":    "",
		"LOCATION_RETENTION_DAYS": "",
		"TRUSTED_PROXIES":         "",
		"ADDR":                    "",
		"FAMILY_NAME":             "",
		"DPC_COMPONENT":           "",
		"LOG_LEVEL":               "",
	}
	for k, v := range overrides {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

func TestLoadAcceptsAValidEnvironment(t *testing.T) {
	validEnv(t, nil)
	c, err := Load()
	if err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	if c.OIDCIssuer != DefaultOIDCIssuer {
		t.Fatalf("issuer default not applied: %q", c.OIDCIssuer)
	}
	if c.OIDCJWKSURL != DefaultOIDCJWKSURL {
		t.Fatalf("jwks default not applied: %q", c.OIDCJWKSURL)
	}
	if c.RateLimitPerMinute != 120 || c.MaxBodyBytes != 1<<20 {
		t.Fatalf("numeric defaults not applied: %d %d", c.RateLimitPerMinute, c.MaxBodyBytes)
	}
	if c.SessionTTL <= 0 || c.CommandTTL <= 0 || c.DeviceOfflineAfter <= 0 {
		t.Fatal("a duration defaulted to zero, which would make the corresponding check vacuous")
	}
	if len(c.AllowedOrigins) != 0 {
		t.Fatalf("empty ALLOWED_ORIGINS must mean same-origin only, got %v", c.AllowedOrigins)
	}
	if c.OAuthAuthURL != DefaultOAuthAuthURL || c.OAuthTokenURL != DefaultOAuthTokenURL {
		t.Fatalf("oauth endpoint defaults not applied: %q %q", c.OAuthAuthURL, c.OAuthTokenURL)
	}
	// Zero here would mean "delete everything older than now", so the defaults are asserted rather
	// than assumed: a retention sweep that silently keeps nothing is worse than one that never runs.
	if c.AuditRetentionDays != 365 || c.LocationRetention != 30*24*time.Hour {
		t.Fatalf("retention defaults not applied: %d days, %v", c.AuditRetentionDays, c.LocationRetention)
	}
	if c.MaintenanceInterval <= 0 {
		t.Fatal("maintenance interval defaulted to zero, which would spin the sweep in a tight loop")
	}
	if c.FamilyName == "" {
		t.Fatal("family name defaulted to empty, which the console would render as a blank title")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		mustMention string
	}{
		{"no database url", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL"},
		{"no oauth client id", map[string]string{"OAUTH_CLIENT_ID": ""}, "OAUTH_CLIENT_ID"},
		{"no oauth client secret", map[string]string{"OAUTH_CLIENT_SECRET": ""}, "OAUTH_CLIENT_SECRET"},
		{"relative oauth auth url", map[string]string{"OAUTH_AUTH_URL": "/authorize"}, "OAUTH_AUTH_URL"},
		{"relative oauth token url", map[string]string{"OAUTH_TOKEN_URL": "/token"}, "OAUTH_TOKEN_URL"},
		{"no signing key", map[string]string{"SESSION_SIGNING_KEY": ""}, "SESSION_SIGNING_KEY"},
		{"short signing key", map[string]string{"SESSION_SIGNING_KEY": "tooshort"}, "SESSION_SIGNING_KEY"},
		{"no public url", map[string]string{"PUBLIC_URL": ""}, "PUBLIC_URL"},
		{"relative public url", map[string]string{"PUBLIC_URL": "/guard"}, "PUBLIC_URL"},
		// PUBLIC_URL is copied into the provisioning payload as server_url, where the same https
		// rule applies. Refused at startup rather than at the QR request a parent makes while
		// standing over a wiped phone.
		{"cleartext public url", map[string]string{"PUBLIC_URL": "http://guard.example.ch"}, "PUBLIC_URL"},
		{"cleartext apk url", map[string]string{"APK_URL": "http://guard.example.ch/app.apk"}, "APK_URL"},
		{"relative apk url", map[string]string{"APK_URL": "/app.apk"}, "APK_URL"},
		{"wildcard origin", map[string]string{"ALLOWED_ORIGINS": "*"}, "ALLOWED_ORIGINS"},
		{"origin with a path", map[string]string{"ALLOWED_ORIGINS": "https://guard.example.ch/app"}, "ALLOWED_ORIGINS"},
		{"origin without a scheme", map[string]string{"ALLOWED_ORIGINS": "guard.example.ch"}, "ALLOWED_ORIGINS"},
		{"non-numeric rate limit", map[string]string{"RATE_LIMIT_PER_MINUTE": "many"}, "RATE_LIMIT_PER_MINUTE"},
		{"zero rate limit", map[string]string{"RATE_LIMIT_PER_MINUTE": "0"}, "RATE_LIMIT_PER_MINUTE"},
		{"tiny body limit", map[string]string{"MAX_BODY_BYTES": "10"}, "MAX_BODY_BYTES"},
		{"bad bootstrap email", map[string]string{"BOOTSTRAP_PARENT_EMAILS": "not-an-email"}, "BOOTSTRAP_PARENT_EMAILS"},
		{"empty issuer", map[string]string{"OIDC_ISSUER": " "}, "OIDC_ISSUER"},
		{"relative jwks url", map[string]string{"OIDC_JWKS_URL": "/certs"}, "OIDC_JWKS_URL"},
		{"bad trusted proxy", map[string]string{"TRUSTED_PROXIES": "not-an-address"}, "TRUSTED_PROXIES"},
		{"zero audit retention", map[string]string{"AUDIT_RETENTION_DAYS": "0"}, "AUDIT_RETENTION_DAYS"},
		{"zero location retention", map[string]string{"LOCATION_RETENTION_DAYS": "0"}, "LOCATION_RETENTION_DAYS"},
		{"apk path that does not exist", map[string]string{"APK_PATH": "/nowhere/familyguard.apk"}, "APK_PATH"},
		{"apk cert path that does not exist", map[string]string{"APK_CERT_PATH": "/nowhere/signing.der"}, "APK_CERT_PATH"},
		// The QR builder refuses a payload with no checksum, so this combination is a server that
		// starts, looks configured, and fails the one action provisioning depends on.
		{"apk url with no checksum source",
			map[string]string{"APK_URL": "https://guard.example.ch/familyguard.apk"}, "APK_PATH"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validEnv(t, tc.env)
			c, err := Load()
			if err == nil {
				t.Fatalf("accepted an invalid configuration: %+v", c)
			}
			if c != nil {
				t.Fatal("a config was returned alongside an error")
			}
			if !strings.Contains(err.Error(), tc.mustMention) {
				t.Fatalf("error does not name the offending setting %q: %v", tc.mustMention, err)
			}
		})
	}
}

// TestLoadReportsEveryProblemAtOnce: fixing a deployment one restart per mistake is how a
// misconfiguration survives a maintenance window.
func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	validEnv(t, map[string]string{
		"DATABASE_URL":        "",
		"OAUTH_CLIENT_ID":     "",
		"SESSION_SIGNING_KEY": "",
		"PUBLIC_URL":          "",
	})
	_, err := Load()
	if err == nil {
		t.Fatal("accepted a configuration missing everything")
	}
	for _, want := range []string{"DATABASE_URL", "OAUTH_CLIENT_ID", "SESSION_SIGNING_KEY", "PUBLIC_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error omits %q, so a deployer would need another restart to find it: %v", want, err)
		}
	}
}

// The loopback exception, stated once so it cannot be tightened by accident.
//
// A device can never reach 127.0.0.1, so allowing cleartext there weakens no deployment — and it is
// the only way the end-to-end suite can run the real binary, with the real payload builder, in the
// shape where the server hosts its own APK. This rule lives in the provisioning package; the case
// exists here because config is where a stricter second copy of it was, and where one could return.
func TestLoadAcceptsCleartextOnlyOnLoopback(t *testing.T) {
	apk := filepath.Join(t.TempDir(), "familyguard.apk")
	if err := os.WriteFile(apk, []byte("not really an apk"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	validEnv(t, map[string]string{
		"PUBLIC_URL": "http://127.0.0.1:8080",
		"APK_URL":    "http://127.0.0.1:8080/dpc.apk",
		"APK_PATH":   apk,
	})
	if _, err := Load(); err != nil {
		t.Fatalf("a loopback deployment was refused: %v", err)
	}

	// The negative half in the same test: the identical shape on a routable host is refused. Without
	// it, a rule that accepted every http URL would pass the assertion above.
	validEnv(t, map[string]string{
		"PUBLIC_URL": "http://guard.example.ch",
		"APK_URL":    "http://guard.example.ch/dpc.apk",
		"APK_PATH":   apk,
	})
	c, err := Load()
	if err == nil {
		t.Fatalf("cleartext was accepted on a routable host: %+v", c)
	}
	for _, want := range []string{"PUBLIC_URL", "APK_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %s: %v", want, err)
		}
	}
}

func TestLoadAcceptsOptionalSettings(t *testing.T) {
	apk := filepath.Join(t.TempDir(), "familyguard.apk")
	if err := os.WriteFile(apk, []byte("not really an apk"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	validEnv(t, map[string]string{
		"APK_URL":                 "https://guard.example.ch/familyguard.apk",
		"APK_PATH":                apk,
		"ALLOWED_ORIGINS":         "https://guard.example.ch, https://console.example.ch",
		"BOOTSTRAP_PARENT_EMAILS": "a@example.com, b@example.com",
		"OIDC_ISSUER":             "https://issuer.example/",
		"OIDC_JWKS_URL":           "https://issuer.example/jwks",
		"RATE_LIMIT_PER_MINUTE":   "600",
		"MAX_BODY_BYTES":          "2097152",
		"AUDIT_RETENTION_DAYS":    "90",
		"LOCATION_RETENTION_DAYS": "7",
		"TRUSTED_PROXIES":         "10.1.0.0/16, 192.168.1.1",
	})
	c, err := Load()
	if err != nil {
		t.Fatalf("valid optional settings rejected: %v", err)
	}
	if c.OIDCIssuer != "https://issuer.example" {
		t.Fatalf("trailing slash not trimmed from the issuer: %q", c.OIDCIssuer)
	}
	if len(c.AllowedOrigins) != 2 || len(c.BootstrapEmail) != 2 || len(c.TrustedProxies) != 2 {
		t.Fatalf("list parsing dropped entries: %+v", c)
	}
	if c.RateLimitPerMinute != 600 || c.MaxBodyBytes != 2097152 {
		t.Fatalf("numeric overrides not applied: %d %d", c.RateLimitPerMinute, c.MaxBodyBytes)
	}
	if c.APKURL == nil || c.APKURL.Scheme != "https" {
		t.Fatalf("apk url not parsed: %+v", c.APKURL)
	}
	if c.AuditRetentionDays != 90 || c.LocationRetention != 7*24*time.Hour {
		t.Fatalf("retention overrides not applied: %d days, %v", c.AuditRetentionDays, c.LocationRetention)
	}
}
