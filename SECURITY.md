# Security

FamilyGuard runs with **Device Owner** rights on a child's phone and holds a family's location
history, app inventory and screen-time record in one database. That is a small system with a large
blast radius, so this document states what it defends, what it does not, and what an attacker gets
from each boundary — including the ones it loses.

## Reporting a problem

Use GitHub's private vulnerability reporting: the repository's **Security** tab → *Report a
vulnerability*. That opens a draft advisory visible only to the maintainer, which is the right shape
for anything that would give someone else's device or data away.

Please do not open a public issue for such a report. A public issue is a working exploit with a
timestamp, aimed at whoever is running this and has not updated yet.

There is no bounty and no SLA. This is a household project maintained in spare time; reports are
still read and acted on, and a fix ships as a new tag rather than as a moved one.

Anything that is not a vulnerability — a bug, a wrong assumption, a phone that behaves differently
from what is documented — belongs in a public issue, where it is more useful.

## The three properties that outrank everything else

1. **The phone must stay recoverable.** `no_factory_reset` is in `FORBIDDEN_RESTRICTIONS` — never
   set, and cleared on every sync if anything else set it, together with `no_outgoing_calls`,
   `no_sms` and `no_create_windows`. A parent can always factory-reset the device back to a working
   phone. No bug in this system, and no policy it can be told to publish, closes that hatch. If a
   change would make the device unrecoverable, the change is wrong, not the requirement.
2. **The server never fabricates success.** A command is a row with a lifecycle. "Sent" is not
   "done"; only a device acknowledgement moves a command to done, and an unreachable phone leaves it
   pending rather than reporting a result nobody produced.
3. **A control that evaluates nothing is a vulnerability, not a gap.** Every guard here has been
   broken deliberately and observed red before being trusted (see `IMPLEMENTATION_PLAN.md`). An
   uncalibrated check is treated as absent — it is left out of the aggregate suite rather than
   allowed to report green.

## Trust boundaries

### Parent → control plane

Sign-in is Google OAuth. The ID token is **verified, never trusted**: RS256 against the live JWKS
(cached, keyed by `kid`), and `iss`, `aud`, `exp` and `email_verified` are all checked. An unknown
`kid`, a token addressed to another client, an expired token or an unverified email are each
rejected, and each is a separate e2e case in `TestIDTokenIsVerifiedNotTrusted`.

Only addresses in `BOOTSTRAP_PARENT_EMAILS`, or a parent an existing parent has added, can sign in.
A verified Google account is not by itself an authorization.

Session tokens are HS256 over `SESSION_SIGNING_KEY`. The key must be ≥32 bytes — the server refuses
to start otherwise, and `NewSessionIssuer` refuses a short key again so that a caller bypassing
config cannot weaken the MAC. Forgery resistance is covered by `TestSessionTokensAreForgeryResistant`.

The token is also **one string**. Base64url spells a 32-byte signature in 43 characters, so its last
character carries two bits that decode to nothing — without strict decoding the same signature has
four spellings and the same credential has four names. `Verify` parses with `jwt.WithStrictDecoding()`
so a non-canonical trailing bit is a rejection: the credential can be compared, logged and revoked by
value, and "one character was changed" means the token changed. This is not a forgery boundary — an
attacker still cannot produce a signature — and the OIDC verifier deliberately does not do it, for
the reason given at that line.

Every rejected credential returns the same opaque error. A probe cannot learn *which* part of a
token was wrong.

### Device → control plane

A device authenticates with an opaque 32-byte bearer token issued once at enrollment. Only its
SHA-256 hash is persisted, so a database dump yields no usable device credential. SHA-256 rather
than a password hash is deliberate: the input is 256 bits of uniform randomness, there is no
dictionary to slow an attacker against, and the lookup is on the hot path of every device request.

Enrollment credentials are **single-use** (`TestEnrollmentCredentialsAreSingleUse`). One device
cannot read or act on another's state (`TestOneDeviceCannotActOnAnother`).

### Browser → control plane

- Content-Security-Policy, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, and a
  `Permissions-Policy` denying geolocation, camera, microphone and payment.
- HSTS is emitted **only** when the request actually arrived over TLS. Sending it over plaintext
  would be a claim the deployment cannot back, and would pin a developer's `http://localhost` to
  https in their browser for a year.
- CORS is an exact-origin allowlist. Empty means same-origin only; there is no wildcard path.
- Request bodies are capped (413). Without a cap, one request can stream until the process runs out
  of memory, and no rate limit helps, because it is one request.
- A bounded LRU rate limiter (429) — bounded because an unbounded per-IP map is itself the memory
  exhaustion it was added to prevent.

`X-Forwarded-For` and `X-Forwarded-Proto` are honoured only from `TRUSTED_PROXIES`. Empty means
nobody, which is the correct default for a server reachable directly.

### Provisioning QR → phone

The QR carries the APK download URL and the **hash of the signing certificate**. The platform
verifies the downloaded APK against that hash before installing it as Device Owner, so a hijacked
download URL cannot install a different app. `TestTheQRPointsAtAnAPKThisServerServes` checks that
the URL in the payload and the APK the server actually serves are the same artifact — a QR minted
against a missing or mismatched file is a checksum for nothing.

Treat a provisioning QR as a credential. It is single-use, but until it is used it is a key to a
phone.

### Offline recovery

A per-device recovery code is five groups of four Crockford-style base32 characters (~100 bits),
with `I`, `L`, `O`, `U`, `0` and `1` omitted so a code read aloud does not depend on telling `O`
from `0`. The device stores only PBKDF2 salt, iteration count and the derived hash — extracting the
DPC's storage does not reveal the code. Verification is constant-time, and a derivation *error*
denies: there is no path where failing to compute the hash lets a code through.

## What this system deliberately does not defend against

Stated plainly, because a security document that implies coverage it does not have is worse than one
that admits a gap.

- **A determined, technical child with physical possession of the phone.** Device Owner is strong
  against a bored teenager, not against an unlocked bootloader. The escape hatch that keeps the
  phone recoverable (property 1) is also, by construction, available to whoever holds it.
- **Custom domain blocking outside the managed browser.** Filtering is DNS-over-TLS at the OS
  resolver plus Chrome's managed `URLBlocklist`. A non-managed browser installed by the child can
  reach a custom-blocked domain the DoT resolver does not itself block. This is CONCEPT.md §2.1, and
  the fix is a self-hosted resolver, which needs port 853 exposed.
- **A compromised control plane.** Whoever runs the server can publish any policy the DPC will
  apply. There is no second signature on a policy bundle. The mitigations are operational: the
  binary runs as an unprivileged user in a distroless image with no shell and a read-only root
  filesystem, and the forbidden-restriction strip is enforced on the **device** as well as on the
  server, so even a hostile bundle cannot block a factory reset.
- **Traffic analysis.** The system does not hide from the network that a phone is managed.

## Secrets

Never commit one, and never paste one into an issue, a commit message, a PR or a log — not even a
prefix. No secret reaches the deployment from this repository: `deploy/` names the Secret it needs
and contains none of it (see [`deploy/secret.example.yaml`](deploy/secret.example.yaml)).

`.gitignore` blocks `.env*`, `*.pem`, `*.key`, `*.p12`, `*.keystore`, `*.jks`, `/.local/` and
`LOCAL-NOTES.md`. That list is not theoretical: the e2e suite generates an RSA keypair for its local
OIDC issuer, and `APK_CERT_PATH` points at a signing certificate. Both exist in the working tree
while the suite runs, so one `git add .` is all it would take.

CI runs [gitleaks](https://github.com/gitleaks/gitleaks) over the **full commit history** on every
push — a directory scan of the current tree would clear a credential by deleting the file that
carried it, which changes nothing for anyone who already cloned. There is deliberately **no
allowlist** — no `.gitleaksignore`, no `paths` exemption. Both are
tempting and both are wrong here. An ignore entry is pinned to `commit:file:rule:line` and is void
the moment history is rewritten, so it decays into a red nobody can reproduce; a `paths` exemption
over `*_test.go` clears one finding by blinding the scanner to every credential anyone ever pastes
into a test. When a fixture trips the scanner, the fix is to make the fixture stop being
secret-shaped — a test that asserts a token's *length* does not need a token's *entropy*.

The DPC's signing keystore is the most valuable secret in the project. It is what makes an APK
installable as Device Owner on an already-enrolled fleet; losing control of it is worse than losing
the database.

## Dependencies

The control plane has four direct dependencies — gin, `golang-jwt/jwt/v5`, `google/uuid` and
`pgx/v5` — and the e2e module has **none at all** — its ID tokens are signed with
`crypto/rsa` rather than with the library the server verifies with, so the suite cannot agree with
that library's bugs by construction.
