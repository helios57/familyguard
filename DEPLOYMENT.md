# Deployment runbook

How FamilyGuard reaches a Kubernetes cluster, in the order the steps have to happen.

This is a **worked example**, not a description of somebody's running system. The manifests it
refers to are in [`deploy/`](deploy/), they render as they stand, and the hostnames, paths and
identifiers in them are placeholders you replace. Nothing in this repository has been deployed
anywhere and no image has been published yet, so every command below is written to be read before it
is run.

**Everything reaches the cluster through git** (NFR-11). Nothing here is applied with `kubectl edit`
or `kubectl patch`. On a GitOps cluster a manual edit is reverted on the next sync, so a change made
that way is not a shortcut — it is a change that disappears. The `kubectl` invocations below are
reads, plus one rollout restart that touches no state the sync owns.

The document is the long way round on purpose. The steps a sync **cannot** do for itself are the
ones that fail silently, so they come first, and each says what its own absence looks like — because
"the pod will not start" is the same sentence for six different causes.

---

## What is being deployed

| | |
|---|---|
| Namespace | `familyguard` — its own, sharing nothing with whatever else is on the cluster |
| Host | `guard.example.com`, TLS by cert-manager |
| Manifests | [`deploy/`](deploy/) — six files; render with `kubectl kustomize deploy` |
| Image | `ghcr.io/helios57/familyguard-control-plane`, pinned by semver, never `:latest` |
| Database | `postgres:18.6`, `hostPath /srv/familyguard/postgres`, uid/gid 50140 |
| APK | `hostPath /srv/familyguard/apk`, mounted read-only at `/srv/apk` |
| App catalog | `hostPath /srv/familyguard/apps`, mounted **writable** at `/srv/apps` — optional, see §3 |

Three `hostPath`s rather than PersistentVolumeClaims, because the reference target is a single node
and a `hostPath` is an honest description of what a single-node volume is. On a cluster with more
than one node, replace all three with PVCs; that is the one substitution in `deploy/` that is not a
string. The catalog one is `ReadWriteOnce`-shaped like the others but is the only volume the control
plane writes to, so on a multi-node cluster it also pins the pod.

## Order of operations

The order matters. Steps 1–6 happen out of band; the apply is step 7, and it fails if any of them
is missing — deliberately, because each failure it would otherwise produce is silent.

### 1. DNS

Point `guard.example.com` at the cluster's ingress address, the same way the other hosts on this
cluster resolve. cert-manager cannot issue until the ACME HTTP-01 challenge resolves, and an
`Ingress` waiting on a certificate looks identical to one that is misconfigured.

Verify from off-cluster before continuing:

```bash
dig +short guard.example.com
```

### 2. The PostgreSQL data directory, pre-`chown`ed

`k8s-node` throughout is the node that will hold the two `hostPath` directories — on a single-node
cluster, the node.

```bash
ssh k8s-node 'sudo mkdir -p /srv/familyguard/postgres &&
             sudo chown 50140:50140 /srv/familyguard/postgres &&
             sudo chmod 700 /srv/familyguard/postgres'
```

The pod runs as uid/gid 50140 — a dedicated identity, deliberately not shared with any other app on
the host.
There is deliberately **no fix-permissions init container**: it would have to run as root, which is
a larger hole than the problem it closes. Without the `chown`, postgres exits at startup on
`could not create lock file` / `data directory has wrong ownership`, which reads like a broken image.

`chmod 700` is not cosmetic — postgres refuses to start if the data directory is group- or
world-readable.

### 3. The APK directory, and the two files in it

```bash
ssh k8s-node 'sudo mkdir -p /srv/familyguard/apk'
```

Then build, sign and place the DPC (see [Building and signing the DPC](#building-and-signing-the-dpc)
below). Two files must be present **before the first sync**:

| File | What it is |
|---|---|
| `/srv/familyguard/apk/familyguard.apk` | the signed release APK, served at `/dpc.apk` |
| `/srv/familyguard/apk/familyguard.der` | its signing certificate, DER-encoded |

The server `stat`s both at startup and **refuses to start** if either is missing. That is the
intended behaviour: the alternative is a console that comes up healthy and whose "Setup QR" button
returns a 500 in front of a parent holding a factory-reset phone.

Both are needed, not one. The APK's own hash goes into the QR as
`PROVISIONING_DEVICE_ADMIN_PACKAGE_CHECKSUM`, which Android deprecated; the certificate's hash goes
in as `PROVISIONING_DEVICE_ADMIN_SIGNATURE_CHECKSUM`, which is what Android 7+ actually verifies.
A QR carrying only the first is one that provisioning refuses.

Neither checksum is ever typed into configuration. Both are computed from the bytes on disk at
startup, because a checksum that is typed rather than computed verifies whatever it was typed from —
which, after the next rebuild, is nothing.

#### The catalog directory, if this deployment hosts applications

FR-16 lets a parent install other applications on a child's phone — a music player, a homework app.
It is **optional**: leave `APK_DIR` unset and the console says the server is not set up to host
applications, in words, instead of showing controls that would 404.

To enable it, create a *second* directory, writable by the container's user:

```bash
ssh k8s-node 'sudo mkdir -p /srv/familyguard/apps && sudo chown 65532:65532 /srv/familyguard/apps'
```

Three things about that command are load bearing:

- **A second directory, not a subdirectory of `apk/`.** `/srv/apk` is mounted read-only, because
  nothing should be able to overwrite the DPC the provisioning QR's checksum was computed from.
- **`chown 65532`** — the distroless `nonroot` uid the pod runs as. The container's root filesystem
  is read-only and this is the only writable path it has. The server `stat`s the directory at
  startup, writes a probe file into it, and **refuses to start** if either fails, so a
  root-owned directory is a pod that never becomes ready rather than a parent who uploads 30 MB
  over a phone connection and is told "internal error" at the end of it.
- The manifest declares it `type: Directory`, not `DirectoryOrCreate`, for exactly that reason: an
  auto-created one is owned by root and the check above would then refuse.

Then there are three ways an APK gets into the catalog, and all three put the file here:

| Route | Who | When |
|---|---|---|
| **Upload** in the console — Apps → *Manage catalog* | any admin parent | the normal way |
| `POST /api/v1/apps` with the APK as the body or as a `multipart/form-data` `apk` part | an admin parent's session **or an API key** (FR-17) | scripts, an MCP server |
| **Scan the server folder** — copy `.apk` files in over SSH, then press the button | any admin parent | a large file, or a batch |

Nothing is trusted from the filename. The package name, version, minimum SDK and the SHA-256 of the
signing certificate are all read out of the archive, and the first build of a package **pins** its
signer: a later build signed with a different key is refused rather than installed. The DPC's own
package is refused outright — it has its own update path.

Two environment variables bound the size, and they have to agree with the ingress:

| Variable | Default | What it is |
|---|---|---|
| `APK_DIR` | *(unset — feature off)* | where the catalog's files live |
| `MAX_UPLOAD_BYTES` | `268435456` (256 MB) | the cap on the upload endpoint only |

`MAX_UPLOAD_BYTES` is separate from `MAX_BODY_BYTES` rather than a raise of it: the general cap
exists so that an unauthenticated request is cheap to refuse, and raising it to fit an APK would
make every endpoint an easy way to make this server read a quarter of a gigabyte.

The ingress caps the request first. `deploy/ingress.yaml` sets `proxy-body-size: 64m`, so with the
defaults **nginx** 413s at 64 MB and the server's own 256 MB limit is never reached — and nginx's
413 carries none of the server's message. Set the two to the same number. 64 MB is a reasonable
ceiling for a family: it holds any APK a parent would sensibly install and is well under what a
phone will download over a home connection.

### 4. The application secret

Everything the control plane needs that is not in a manifest arrives in **one** Kubernetes Secret
named `familyguard-secret`. [`deploy/secret.example.yaml`](deploy/secret.example.yaml) is the
contract — its five keys and what each one has to contain. That file is deliberately *not* part of
the kustomization: applying it as it stands would deploy placeholder credentials, which is worse than
failing.

| Key | How to produce it |
|---|---|
| `SESSION_SIGNING_KEY` | `openssl rand -base64 48`. Never a passphrase — the server refuses anything under 32 bytes rather than deriving a key from a weak one |
| `OAUTH_CLIENT_ID` | from the Google OAuth client (§5) |
| `OAUTH_CLIENT_SECRET` | from the same client |
| `POSTGRES_PASSWORD` | `openssl rand -base64 32` |
| `DATABASE_URL` | the DSN, composed **here** from `POSTGRES_PASSWORD`, so the password never appears in a manifest, a pod spec, or the output of `kubectl describe` |

**How that Secret gets into the cluster is your decision, and it is the one part of this runbook that
cannot be copied.** Committing it is not an option. External Secrets, Sealed Secrets, SOPS and
Infisical all satisfy the contract equally well; the reference deployment materialises
`familyguard-secret` from a secret manager through an operator, which is why `deploy/` names the
Secret everywhere and contains it nowhere.

Whichever you choose, two properties are not optional:

- **The pods must restart when the Secret changes.** A container reads its environment once, at
  exec. Rotate a password behind a running pod and it holds the old value in memory indefinitely —
  which reads as "the rotation did not take" long after it did, and the next restart, whenever that
  is, turns into an outage nobody connects to the rotation. Both Deployments in `deploy/` carry
  `reloader.stakater.com/auto`; if you do not run
  [Reloader](https://github.com/stakater/Reloader), drop the annotation and roll the pods yourself.
- **A secrets operator can succeed partially.** If it templates a key that does not exist in the
  backing store, what you get is a Secret that exists and is missing one entry — so the sync is
  green and the pod fails on the single variable that is absent. Read the key names back after the
  first apply (see [Verifying the first apply](#verifying-the-first-apply)); never print the values.

There is **no pull credential** in that list, and its absence is a decision rather than an omission:
this repository is public, so its GHCR package is public, so the kubelet pulls anonymously and
`deploy/control-plane.yaml` carries no `imagePullSecrets`. Fork it privately and that stops being
true — the pod then fails with `unauthorized`, which is one `kubectl describe pod` away from being
distinguishable from a tag that does not exist (§6).

Never print a secret's value, not even a prefix — not into a terminal that is being recorded, not
into a commit message, not into an issue.

### 5. The Google OAuth client

In the Google Cloud console, an OAuth 2.0 **Web application** client:

- Authorised JavaScript origin: `https://guard.example.com`
- Authorised redirect URI: `https://guard.example.com/api/v1/auth/google/callback`

`BOOTSTRAP_PARENT_EMAILS` in `deploy/control-plane.yaml` is `parent@example.com` in the example. Only that
address can sign in until it invites another parent. A verified Google account is not by itself an
authorization — this is what stops a fresh database from being claimable by whoever finds the URL.

### 6. Publish the image

```bash
git tag vX.Y.Z && git push origin vX.Y.Z    # X.Y.Z is what deploy/control-plane.yaml pins
```

`.github/workflows/release.yml` refuses the tag unless it is a plain semver, on `main`, whose version
matches the one the Android app carries; re-runs every CI layer against the tagged tree; and only
then publishes `ghcr.io/helios57/familyguard-control-plane:X.Y.Z`. Three numbers move together and
the tag is refused if any of them disagrees: the tag, the Android app's version, and the image the
deployment manifest pins. Writing them as a version here instead of as a rule is how they drift.

`0.1.0` was the first release, on 2026-08-18. The package is public — an anonymous manifest request
for a released version answers 200, and `latest` answers 404 because this workflow deliberately
publishes no floating tag. `deploy/control-plane.yaml` pins the version the tag on that same commit
publishes, so between the commit and the end of the release run the pin names an image that does not
exist yet — an apply in that window sits in `ImagePullBackOff`, and `kubectl describe pod`
distinguishes the two causes of that one symptom: `manifest unknown` is a tag that was never
published, `unauthorized` is a missing credential.

The job prints the **digest**. That is the artifact; the tag is a label on it, and a label can move.
Verify a running pod against the digest, never against the tag it was pulled by — and never move a
published tag onto a fix, because a release tag that moves is the one change a consumer cannot
detect.

Every image carries BuildKit's in-registry `provenance` and `sbom` attestation manifests, which
travel with the image and need no GitHub feature:

```bash
docker buildx imagetools inspect ghcr.io/helios57/familyguard-control-plane:0.1.0 \
  --format '{{ json .Provenance }}'
```

The Sigstore half — `gh attestation verify` — is separate, and it needs three things at once:
`id-token: write` to sign, `attestations: write` to store, and a repository GitHub will accept one
for at all (it stores none for user-owned **private** repositories, at any permission). Both
refusals land on the same workflow step, *after* the image has already been pushed, and the second is
invisible until the first is fixed. The workflow therefore reads the repository's visibility back
from the API rather than assuming it, skips the step when it cannot succeed, and prints in its own
summary which of the two provenances the image ended up carrying — because a skipped step is green,
and an absence that is not announced reads as a success.

### 7. Apply the manifests

Render first, and read the render — `kubectl kustomize` is the only step here that fails loudly:

```bash
kubectl kustomize deploy          # Namespace, 2 Services, 2 Deployments, Ingress
```

Then let git do the applying. On an ArgoCD cluster that means committing `deploy/` (or a kustomize
overlay on top of it) into whatever repository your `Application`s are generated from, and letting
the sync run. `kubectl apply -k deploy` works too and is the right thing on a cluster that has no
GitOps controller — but on one that does, it is reverted within minutes, which looks exactly like the
deployment silently breaking itself.

Point the `Application` at `deploy/` unchanged if the defaults suit you. If they do not — and the
hostname at least will not — a kustomize overlay that patches `deploy/` is better than a fork of it,
because an overlay shows in one file exactly which values are yours.

Do not attach an image-updater to this app. The version that runs is bumped by an edit to
`deploy/control-plane.yaml`, which is what makes the git history the answer to "which version was
deployed, and when".

## Verifying the first apply

Read state back from the authority. `git push` printing nothing is not proof, and neither is a green
ArgoCD tile — a controller reports that it applied what it was given, not that what it applied works.

```bash
kubectl -n familyguard get pods,svc,ingress
kubectl -n familyguard get secret familyguard-secret -o jsonpath='{.data}' | tr ',' '\n' | cut -d'"' -f2
```

The second command lists the **key names** in the synced Secret and never their values. Expect
`SESSION_SIGNING_KEY`, `OAUTH_CLIENT_ID`, `OAUTH_CLIENT_SECRET`, `POSTGRES_PASSWORD`,
`DATABASE_URL`. A missing key means whatever populates the Secret referenced something the backing
store does not have — and that is reported as a *partial* success, so the Secret appears and the pod
fails on the one variable that is absent (§4).

Then confirm the image by digest rather than by tag:

```bash
kubectl -n familyguard get pod -l app=familyguard-control-plane \
  -o jsonpath='{.items[*].status.containerStatuses[*].imageID}'
```

Compare that digest with the one the release workflow printed (§6). Equal digests mean the cluster
is running the code that was tested. A matching *tag* means nothing — a tag can be moved.

Finally, over the network rather than from inside the cluster:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://guard.example.com/healthz
curl -sS -o /dev/null -w '%{http_code}\n' https://guard.example.com/readyz
```

`/healthz` answers as soon as the process is listening. `/readyz` pings the database. A 200 from the
first and a 503 from the second is a control plane that started and cannot reach postgres — which
would serve 500s to every phone while a liveness probe on `/healthz` alone called it healthy
forever.

---

## Backups

`deploy/backup.yaml` dumps the database nightly and keeps 30 days. Read a run with
`kubectl -n familyguard logs job/<name>`; the job's exit status is the alert, because a backup that
fails quietly is the same as no backup at all.

It restores what it dumped, in the same job, before the file is allowed to be called a backup —
size, `pg_restore --list`, a real restore into a throwaway database with `--exit-on-error`, and a
table-by-table row-count comparison against the live database. Only then is the `.tmp` renamed into
place. The scratch database is dropped by an EXIT trap, so a failed run does not leave the next one
failing on a name collision instead of on the real fault.

**Before the first run**, the directory must exist and be owned by the same uid as the database,
for the same reason the data directory must: the pod is unprivileged and cannot chown it.

```bash
sudo install -d -m 0700 -o 50140 -g 50140 /srv/familyguard/backups
```

**Restoring.** Stop the control plane first — a restore into a database something is writing to is
a race you will not notice losing.

```bash
kubectl -n familyguard scale deploy/familyguard-control-plane --replicas=0
kubectl -n familyguard exec deploy/familyguard-db -- \
  pg_restore -U familyguard -d familyguard --clean --if-exists --exit-on-error \
             /backup/familyguard-<stamp>.dump
kubectl -n familyguard scale deploy/familyguard-control-plane --replicas=1
```

`--exit-on-error` is not optional here either: without it `pg_restore` reports every error and still
exits 0, and a partial restore looks exactly like a complete one.

**What the nightly job does not do.** The dumps land on the same storage as the data. That covers a
bad migration and a `DELETE` without a `WHERE`; it does not cover losing the machine, and RAID is
not a backup — it survives one disk, not one wrong command. Copying them somewhere else is a job on
another machine, and nothing in this repository can claim to have done it for you.

---

## Building and signing the DPC

There is deliberately **no `signingConfig` in `app/build.gradle.kts`**. A signing config in the build
file means a keystore path in the repository and a password in a properties file or an environment
variable; keeping the whole operation out of band means the keystore is never a build input.

Create the keystore once, and never again — every phone already enrolled will only accept an
update signed by this key:

```bash
umask 077 && mkdir -p ~/.familyguard
openssl rand -base64 33 | tr -d '\n' > ~/.familyguard/keystore-password
keytool -genkeypair -keystore ~/.familyguard/familyguard-release.jks -storetype PKCS12 \
  -storepass:file ~/.familyguard/keystore-password -keypass:file ~/.familyguard/keystore-password \
  -alias familyguard -keyalg RSA -keysize 4096 -sigalg SHA256withRSA -validity 10950 \
  -dname "CN=FamilyGuard DPC, OU=FamilyGuard, O=io.github.helios57, C=CH"
```

Then, for each release:

```bash
cd android-dpc
./gradlew :app:assembleRelease
# → app/build/outputs/apk/release/app-release-unsigned.apk

BT=$ANDROID_HOME/build-tools/37.0.0
$BT/zipalign -p -f 4 app/build/outputs/apk/release/app-release-unsigned.apk /tmp/aligned.apk

# The password goes through the environment, not through `file:`. apksigner's file reader takes
# the store password from line 1 and the key password from line 2 OF THE SAME FILE, so a
# single-line password file fails with "end of file reached" — naming the key, which reads like a
# wrong alias rather than a password that was never supplied.
export KSPASS="$(cat ~/.familyguard/keystore-password)"
$BT/apksigner sign --ks ~/.familyguard/familyguard-release.jks \
  --ks-pass env:KSPASS --key-pass env:KSPASS --ks-key-alias familyguard \
  --v4-signing-enabled false --out /tmp/familyguard.apk /tmp/aligned.apk
$BT/apksigner verify --print-certs /tmp/familyguard.apk
```

`apksigner verify` reporting **v3 only** is correct here, not a missing scheme: v1 (JAR) is
required below API 24 and v2 below API 28, and `minSdk` is 29. v4 is off because it produces a
separate `.idsig` that only `adb install --incremental` consumes; nothing in this flow does.

Export the signing certificate in DER form — this is the file whose SHA-256 becomes the QR's
signature checksum:

```bash
keytool -exportcert -keystore ~/.familyguard/familyguard-release.jks \
  -storepass:file ~/.familyguard/keystore-password -alias familyguard -file /tmp/familyguard.der
```

**Cross-check the two before shipping them.** `sha256sum /tmp/familyguard.der` must equal the
`certificate SHA-256 digest` that `apksigner verify --print-certs` just printed for the APK:

```bash
sha256sum /tmp/familyguard.der
```

They come from different tools reading different files, and nothing downstream compares them. If
they disagree, the QR carries a checksum for a certificate the phone never receives, and
provisioning fails on the phone, mid-setup, as "Can't set up device" with no further detail —
the one failure in this system that cannot be debugged from the server side.

Then copy both onto the host:

```bash
scp /tmp/familyguard.apk /tmp/familyguard.der k8s-node:/tmp/
ssh k8s-node 'set -e
  D=/srv/familyguard/apk
  # Keep the build being replaced. It is what provisioned every phone enrolled so far, and it is
  # the only thing to roll back to if the new one turns out not to install. Name it for the version
  # it holds — `familyguard-0.1.0-versionCode-1.apk` — not "previous", which stops being true on
  # the next replacement.
  sudo install -D -m 0644 "$D/familyguard.apk" \
    /srv/familyguard/apk-archive/familyguard-<versionName>-versionCode-<n>.apk
  # Rename into place rather than write over the served file: a phone may be downloading it at this
  # moment, and a rename is atomic — a reader that already opened the file keeps the whole old one.
  sudo install -m 0644 /tmp/familyguard.apk "$D/.familyguard.apk.incoming"
  sudo mv -f "$D/.familyguard.apk.incoming" "$D/familyguard.apk"
  sudo install -m 0644 /tmp/familyguard.der "$D/familyguard.der"
  rm /tmp/familyguard.apk /tmp/familyguard.der'
```

Then restart the control plane — and the reason it is not optional is worth reading, because
skipping it does not leave the old build serving quietly. The server hashes the APK **once at
startup**, and that value is what every provisioning QR and every `UPDATE_APP` response carries. A
pod still running across the swap therefore publishes a checksum for bytes it no longer has, so
`/dpc.apk` stops serving the file at all and answers `503 apk_changed` with that reason in the body
until it is restarted. That refusal is deliberate, and it is the good outcome: the alternative is a
phone rejecting a download it just made, mid-provisioning, showing a parent nothing but "Can't set
up device".

**If the deployment is reconciled from git, do not restart it by hand.** A `kubectl rollout restart`
writes an annotation the next sync removes again, and it leaves nothing behind that says which build
the node serves. Record the APK's SHA-256 as a pod-template annotation instead, and update it in the
same commit that replaces the file — changing the pod template is what restarts the pod, and the
annotation is then a claim about the node that can be checked against it:

```bash
ssh k8s-node sha256sum /srv/familyguard/apk/familyguard.apk   # must equal the annotation
```

Only where nothing reconciles the deployment is the direct restart the right tool:

```bash
kubectl -n familyguard rollout restart deploy/familyguard-control-plane
```

> **The keystore is the most valuable secret in this project.** It is what makes an APK installable
> as Device Owner on an already-enrolled fleet. Losing control of it is worse than losing the
> database. It is git-ignored (`*.keystore`, `*.jks`) and it belongs in a password manager or an
> offline backup, not on the build machine alone.
>
> Losing it is the other half of the same problem, and it is unrecoverable: there is no way to
> re-sign for a phone that is already enrolled, so every device has to be factory-reset and
> provisioned again. Back the file up somewhere that is not this machine, and verify the backup by
> restoring it and comparing bytes — a backup that has never been restored is a claim, not a
> backup.

### Replacing the APK later

The signature checksum does not change as long as the signing key does not, so an updated APK signed
with the same key can be dropped in and the pod restarted.

Enrolled devices are **not** re-enrolled. **From DPC 0.6.2 onward they update themselves** (FR-15.6):
every phone checks `/device/apk-info` two minutes after it connects and every fifteen minutes after
that, compares the version the server publishes with the one it is running, and downloads nothing at
all when it is already current. So replacing the APK and restarting the pod is the whole deploy — the
fleet follows within about a quarter of an hour, and nobody has to press anything.

> **Play Protect gates this on a phone that has never seen the app before, and the automatic path
> has nobody to press *Install anyway*.** Measured 2026-09-07: an install driven by the console
> button, through the DPC's own device-owner session, still raised *"FamilyGuard … is not known"*.
> Nothing in the app can turn that off — see `IMPLEMENTATION_PLAN.md` §17.12 for what was checked.
> Either turn *Scan apps with Play Protect* off on the handset (Play Store → profile → Play Protect
> → settings; a user toggle, one step per phone), or distribute the APK through Google Play so it
> stops being unknown — **keeping the existing signing key**, because a certificate change makes
> every enrolled phone refuse the update. Until one of those, an unattended update comes back
> `STATUS_FAILURE_BLOCKED` and says so on the device card.

> **0.6.0 and 0.6.1 shipped that loop and it did not run.** Its cadence was a coroutine `delay`,
> which is measured on a clock that stops while the phone is asleep, so on a real handset the first
> two-minute check had not elapsed after 48 minutes — no error, no failed download, just a phone that
> looked online and current. 0.6.2 books the check with `AlarmManager` and decides on the wall clock;
> `IMPLEMENTATION_PLAN.md` §17.11 has the measurement. **A phone on 0.6.0 or 0.6.1 cannot take 0.6.2
> by itself** — press **Update app** once, and the fleet is automatic from the build that lands.

**Update app** is still there and still works: it is the shortcut for "now" rather than the
mechanism. It is an ordinary instant command, so it queues while the phone is off and runs when it
comes back.

Two things about that are worth knowing before you use it. The phone refuses anything that is not
strictly newer, not the same package, not signed by the same key, or that does not match the
checksum this server published — so dropping in an APK signed with a different key does not produce
a broken fleet, it produces a refusal with a reason under the command. And the acknowledgement
arrives *before* the install, because the install kills the process that would otherwise send it:
what tells you the update landed is the build number the device reports on its next heartbeat, shown
on the device page.

A phone that acknowledged and never reported a new build **says why, from 0.6.0 onward** (FR-15.7).
The reason is Android's own words and it appears on the device card, above the screen-time notice, as
*"This phone did not take the last update. …"*. It clears itself once the phone reports a newer
build, so it is never stale. Before 0.6.0 there was no channel for it at all: the command showed
acknowledged, the phone kept heartbeating, and the version simply never moved — which looks exactly
like a phone that was already current. That is the defect Phase 17 exists for, and it happened on
this fleet.

The card also shows what the phone could be running: an amber `app 0.5.0` next to a `→ 0.6.0` when it
is behind, and the command button reads **Update to 0.6.0**. Both numbers are measurements — the
phone's from its heartbeat, the server's parsed out of the APK on disk at startup — so when either is
missing the console shows no comparison at all rather than an "up to date" nobody checked.

> **One-time, for phones enrolled on 0.5.0 or earlier.** The fix ships *in* the APK, and those
> builds cannot install one by themselves — that is the bug. Nor can the console help: their baseline
> sets `no_install_unknown_sources` and `no_debugging_features` on every desired state, with no
> policy field behind either, so there is no sideload and no adb for as long as the phone is being
> managed. **The recovery code is the way through, and no factory reset is needed** — it releases
> exactly those restrictions. The order matters, because a phone that reaches the server ends its own
> release: **revoke first, then recover.**
>
> 1. Device card → **Recovery code**, and write it down.
> 2. Device card → **Replace phone** → confirm. The phone is now revoked, which is what makes the
>    next step stick.
> 3. On the phone: FamilyGuard → **Recovery** → the recovery code. Everything lifts.
> 4. In the phone's own browser open **`https://<your host>/dpc.apk`** and install it over the top.
>    Same key, so it is an update: Device Owner, the credential and the app data all survive.
>    **Google Play Protect will stop this and say the app "is not known"** — it is an app Google has
>    never seen, installed by a browser. Choose *Install anyway*; that gate is on the browser path,
>    not on the DPC's own updates.
> 5. Device card → **Replace phone** again for a fresh code (the first has a 30-minute life), then
>    FamilyGuard → **Recovery** → **Re-link this phone**. The next sync re-applies the full policy.
>
> It is the last time either way: 0.6.0 onward updates itself. This route is read out of the 0.4.0
> sources and has not been run on a handset; if step 4 is blocked by something the sources do not
> show, the factory reset in *Enrolling the first phone* still works.

Signing a new APK with a **different** key breaks provisioning for new devices and cannot upgrade
existing ones at all. There is no recovery from a lost signing key other than factory-resetting every
enrolled phone — which the fleet permits, by design, because `no_factory_reset` is never set.

---

## Installing another app on a child's phone

Requires `APK_DIR` (above). The model is a **declared set, not a queue of commands**: you say which
packages a child's phone should have, the phone re-reads that set on every sync, and it installs
what is missing and removes what has been withdrawn. There is no "install" button to press twice,
and no way for the phone and the console to end up disagreeing about what was asked for.

1. **Put the APK in the catalog** — Apps → *Manage catalog* → choose a file → Upload. Or copy
   `.apk` files into `/srv/familyguard/apps` over SSH and press *Scan the server folder*, which is
   the better route for something large.
2. **Turn the switch on** for the child, on the Apps screen. That is the whole declaration.
3. The phone acts on its next sync — immediately if it is online, when it comes back if it is not.

To remove it, turn the switch off. The phone uninstalls it.

Things worth knowing before you rely on it:

- **The phone will put it back.** If the child uninstalls a declared app, the next sync reinstalls
  it. That is convergence, not a command being retried, so it also survives the phone being
  reset and re-enrolled.
- **Only what this DPC installed is ever removed.** The uninstall candidates come from Android's own
  record of which installer put a package there. Withdrawing a declaration cannot touch an app the
  child or the manufacturer installed.
- **A new build replaces the old one automatically.** Upload a newer version and every phone that
  has the package declared upgrades itself. The catalog keeps the older builds; delete them when you
  are sure.
- **The signing key is pinned on first upload.** A rebuild with a different key is refused with
  "signer changed" rather than installed — which is the same rule Android enforces on the phone, met
  earlier and with a message that says what happened.
- **The DPC itself cannot be a catalog entry.** It updates over its own path; see *Replacing the APK
  later*.
- Bedtime, the daily limit and app rules all still apply to a managed app. Installing something is
  not exempting it.

## Getting the vendor's software off the phones

**Family → Apps → "Blocked for everyone".** One list, applied to every child, including any added
later. It ships populated: the four Meta packages a Samsung preinstalls, OneDrive, Link to Windows
and My Galaxy. Nothing needs to be configured for that to take effect — the list applies at the next
sync of every enrolled phone.

What it does is **hide and suspend**, not uninstall. The app stays on the phone, cannot run and does
not appear in the launcher, and removing the entry brings it back at the next sync. That is
deliberate: nothing here can remove something a factory reset would not restore.

- **The three Facebook stubs matter as much as the app.** `com.facebook.system` is a preinstalled
  installer whose job is to put `com.facebook.katana` back. Blocking only the visible app does not
  hold.
- **An entry works on a package the phone does not have yet.** That is the point — it is what stops
  a preinstall arriving later, and the list says "not installed here" beside such an entry rather
  than hiding it.
- **Exceptions are per child.** Set the app's rule to **Allow** on the child who needs it; the rest
  of the family keeps the block. A per-child **Block** is never overruled by the family list.
- **Some packages are refused whatever the list says.** The phone reports its own dialer, launcher,
  settings app, SMS app and every enabled keyboard as critical, and those are stripped from the
  computed policy last and unconditionally (FR-5.5). Putting one on the list has no effect rather
  than a bad one.
- **Removing a preinstalled entry is permanent.** It is recorded, not just absent, so a restart does
  not bring it back.

What is deliberately **not** on the shipped list, because hiding it breaks the phone: Samsung's
software-update client (`com.wssyncmldm` — hiding it stops security patches), Samsung Account, the
two Knox packages that device-owner enrolment runs through, and the caller-ID service the dialer
depends on. Add packages by all means; check first that nothing you rely on is built on them.

The same list is at `GET/PUT/DELETE /api/v1/family/blocked-packages` for a script or an MCP server.
Reading it needs any parent; changing it needs an admin.

---

## API keys, for scripts and MCP servers

Everything a parent can do in the console can be done with an API key instead of a browser session —
including uploading an APK and declaring it for a child.

Create one under **Family → API keys**. The token is shown **once**, at that moment, and is not
recoverable afterwards; only its SHA-256 is stored. It looks like `fgk_…` and goes in the ordinary
header:

```bash
curl -H "Authorization: Bearer fgk_…" https://guard.example.com/api/v1/children
```

- A key **is** the parent that created it — same family, same role, no separate permissions to keep
  in step.
- A key **cannot** create sessions, add parents, or mint or revoke other keys. A stolen key cannot
  grow itself a second foothold that survives being revoked.
- Keys do not expire. Revoke instead; it takes effect on the next request. `last_used_at` on the
  list is what tells you which of them is still in use.
- Revoked keys stay in the list, because "was this ever used, and when did it stop" is the first
  question after a laptop goes missing.
- The audit log records that a key acted, and which one, distinctly from a parent acting in a
  browser.

---

## Enrolling the first phone

1. Sign in to `https://guard.example.com` as a bootstrap parent.
2. Add the child, then add a device for that child. The server mints a single-use provisioning token
   and renders the QR.
3. Factory-reset the phone, or start from the setup wizard on a fresh one.
4. Tap the welcome screen six times to open the QR reader. Join Wi-Fi if prompted.
5. Scan. The phone downloads the APK from `https://guard.example.com/dpc.apk`, verifies it against the
   signature checksum in the QR, and installs it as Device Owner.
6. The DPC enrolls with the one-time token, opens the event stream and applies the policy.
7. **Grant the two appops below.** Provisioning does not, and cannot, grant them.

The token is single-use and a replay is refused. A QR that was displayed and not used is still a key
to a phone until it expires — treat it as a credential.

### The grants Device Owner does not give you

`PACKAGE_USAGE_STATS` and `SCHEDULE_EXACT_ALARM` are **appops**, not runtime permissions. No
device-owner API can set one — `setPermissionGrantState` covers runtime permissions only — so each is
a one-time step per phone. Skipping them fails in opposite ways, and only one of them is loud:

| Appop | If it is missing |
|---|---|
| `GET_USAGE_STATS` (`PACKAGE_USAGE_STATS`) | **Screen time cannot be measured at all.** Every query returns nothing. The DPC reports *not measured* rather than zero, so the console says so instead of showing a child who spent the day off their phone — but the daily limit (FR-3) can never be reached until this is granted. The phone raises an ongoing notification for it; tapping that opens Usage access **scrolled to FamilyGuard's own row, flashing**, so nobody has to find one entry in a list of two hundred. On a build whose Settings does not accept the highlight the phone falls back to the plain list and logs that it did. **The grant is noticed the instant it is made** — the DPC watches the appop, so the notification clears and the first measurement is sent while you are still holding the phone. Before 0.5.0 the only thing that re-read the appop was the sync, so a parent who granted it correctly kept being told it was missing for up to fifteen minutes, and forever on a phone the server had stopped accepting. |
| `SCHEDULE_EXACT_ALARM` | Bedtime still starts, late. The DPC falls back to a wake-up the platform may delay and logs it as `INEXACT`; it does not fall back to no alarm. |

From the phone: **Settings → Apps → Special app access → Usage access → FamilyGuard**, and the same
menu's **Alarms & reminders**.

Over adb, which is what a bench phone should use:

```bash
adb shell appops set io.github.helios57.familyguard GET_USAGE_STATS allow
adb shell appops set io.github.helios57.familyguard SCHEDULE_EXACT_ALARM allow

adb shell appops get io.github.helios57.familyguard GET_USAGE_STATS      # → GET_USAGE_STATS: allow
adb shell appops get io.github.helios57.familyguard SCHEDULE_EXACT_ALARM # → SCHEDULE_EXACT_ALARM: allow
```

Measured on a device-owned Android 14 emulator: before the grants both read `No operations. /
Default mode: default`, after them both read back `allow`. The read-back is worth doing because
`appops set` is not silent about a name it does not know — a misspelled op exits **255** with
`Error: Unknown operation string` — but it *is* silent about a package name it does not know.

The authority on whether the alarm ended up exact is the phone's own log, not this table:

```bash
adb logcat -s FamilyGuard/Connection | grep 'enforcement wake-up'
# sync: next enforcement wake-up at 1755500400000 (exact)     ← granted
# sync: next enforcement wake-up at 1755500400000 (INEXACT)   ← not granted, still enforcing
```

#### And a third, only if the control plane is on the family's own network

`ACCESS_LOCAL_NETWORK` is a runtime permission, so `setPermissionGrantState` ought to cover it. It
does not: measured on an Android 37 emulator on 2026-09-04, the call returns **true** — from a clean
state and from a policy-fixed one — while `checkSelfPermission` stays `DENIED`. The DPC reads the
result back rather than believing it, and says so once per start:

```bash
adb logcat -s FamilyGuard/Connection | grep ACCESS_LOCAL_NETWORK
# android.permission.ACCESS_LOCAL_NETWORK is not held (policy accepted=true) — a control plane on
# the family's own network is unreachable
```

**This costs nothing for the deployment this document describes.** From Android 37 the platform
sorts every destination into *global* or *local* and drops an app's packets to a local one without
this permission; `guard.example.com` over the internet is global, so a phone reaching the control
plane the normal way is unaffected. It matters if you host the control plane at home and the phones
reach it at `192.168.x.x` on the same Wi-Fi — then every request is **dropped, not refused**, the
DPC waits out its 15 s connect timeout, and its log says only that the server did not answer. It
reads exactly like a server that is down, and a shell on the same phone reaches the same address in
the same second, because `adb`'s uid is exempt.

The way out is a grant nobody but the phone's user can make:

```bash
adb shell pm grant io.github.helios57.familyguard android.permission.ACCESS_LOCAL_NETWORK
adb shell dumpsys package io.github.helios57.familyguard |
  grep 'ACCESS_LOCAL_NETWORK: granted'   # → granted=true
```

From the phone: **Settings → Apps → FamilyGuard → Permissions → Local network devices → Allow.**
It survives the DPC re-applying its whole policy, and it survives an app update.

**Device Owner can only be established on an unprovisioned device.** Once the phone has an account
on it, the only route back is a factory reset.

### When a phone stops reporting: re-link it, do not reset it

Asking for a setup code on a phone that is **already enrolled** revokes it. The console spells that
out and makes you confirm it, but if it has happened — or if the credential is broken some other way
— the phone comes back without a factory reset, and without losing its enrollment, its name or its
history. It is the same device row afterwards.

**The *Re-link this phone* field is on the recovery screen whenever the phone holds a credential**,
whether or not anything has gone wrong. It was gated on the `401` flag until 0.5.0, and the gate is
what "how do I relink? I don't see a button" turned out to be: the flag is written by
`ConnectionService` when it personally receives a `401`, so a phone that was revoked while it was
switched off — or one whose service has not managed to run since — showed a parent nothing at all,
on the one screen they had been told to look at. Nothing is spent by offering it: the code is checked
by the server, and a phone that does not need re-linking just gets "that code was not accepted".

What the `401` still does is *explain*: it raises the ongoing **"This phone is no longer linked"**
notification and changes the wording above the field. Only a `401` does that. A `404` or a `409` is a
fault in one request, not a statement about the credential, and raising the alarm for those is how
the one notification that matters gets swiped away.

1. **In the console:** the device's card → **Replace phone** → confirm. The sheet shows the QR *and*
   the same code as type-able text underneath. Take the text: a phone that is already Device Owner
   has no welcome screen left to scan a QR with.
2. **On the phone:** open FamilyGuard → **Recovery** → **Re-link this phone**, type the code,
   **Re-link**. Case matters — the code is base64url.
3. The phone exchanges it, stores the new credential and starts syncing. The console shows it
   enrolled again within a heartbeat.

The code is single-use and expires with the same window as any setup code. If the phone cannot reach
the server yet it says *not now* rather than *wrong code*; the code is still good, so do not generate
another one.

**Two things this cannot do**, and both are deliberate:

- **It cannot move the phone to a different control plane.** The server address comes from the
  credential already on the phone, never from what was typed, so a code is only ever a bearer token.
- **It cannot bring back a phone you meant to cut off.** Re-linking needs a code the server minted,
  which means a parent with console access. A lost phone stays lost.

**A phone running 0.3.0 or older has no re-link screen**, because the DPC never re-enrolled while it
still held a credential — and it cannot get one by updating itself, because FR-15 self-update runs
inside an authenticated sync and an unlinked phone has none. That is a genuine bootstrap trap: the
build that can re-link is exactly the build an unlinked phone cannot reach. For those the way back is
the recovery code first, then a sideload of the current APK, then re-link. **A factory reset is not
required and should not be the first move** — the console said it was until 0.5.0, which is part of
why this looked like a dead end.

Redeeming the recovery code clears every managed restriction, `no_install_unknown_sources` among
them. So, on the phone itself and with no cable and no computer:

**A revoked phone first, and that is why this order works.** The release ends the moment a policy
arrives *from the server* — `Synchronizer.applyFrom` clears recovery mode on a server-sourced policy
and re-applies everything — so on a phone whose credential still works, the release lasts until the
next sync and no longer, which can be less time than a download. A phone that has been unlinked never
gets that far: the `401` returns before any policy is applied, so its release stands until somebody
ends it. If the phone you are recovering is **still linked**, do a **Replace phone** *before* step 1
to revoke it, and the same four steps then hold.

1. FamilyGuard → **Recovery** → type the recovery code. Everything lifts.
2. In the phone's own browser open **`https://<your host>/dpc.apk`** and tap the download to
   install. Same signing key, so it installs **over** the old build: Device Owner and app data both
   survive, and nothing has to be re-provisioned. **Play Protect blocks this the first time**, with
   *"blocked by Play Protect because it's not known"* — the app is not on Play and never will be, so
   choose *Install anyway*. The button on an install over an existing app says **Update**, not
   Install.
3. Console → **Replace phone** for a fresh setup code.
4. FamilyGuard → **Recovery** → **Re-link this phone**, and type it.

The console's *Recovery code* sheet now carries those four steps, so they are in front of the parent
at the moment they need them rather than only here.

If a cable is genuinely easier, the release clears `DISALLOW_DEBUGGING_FEATURES` too and step 2
becomes `adb install -r familyguard-<version>.apk`. That needs developer options switched on by hand
first (Settings → About phone → tap Build number seven times): clearing the restriction lets the
toggle be used, it does not turn it on. From 0.5.0 there is also a per-child **Allow developer
options and USB debugging** switch (FR-5.6) which keeps adb available without a release at all —
useful on a phone you are working on, and off by default on every other, because it is the one
restriction whose absence is how you get back in when a sync stops happening.

A release survives a reboot from 0.4.0 onward (FR-12.6). On 0.3.0 it does not — the boot receiver
re-applied the baseline over a released device — so on an older build do the sideload before the
phone restarts.

---

**And it can only be removed by one.** `adb shell dpm remove-active-admin` refuses the shipping DPC
— *`SecurityException: Attempt to remove non-test admin`*, measured, because the release build is
not `testOnly`. There is no adb route out of Device Owner and no in-app one either: the DPC calls no
`clearDeviceOwnerApp`. That is the whole reason `no_factory_reset` is in `FORBIDDEN_RESTRICTIONS` —
the reset is not one way out among several, it is the way out.

---

## Rollback

| Situation | What to do |
|---|---|
| A bad image version | Edit the tag in `deploy/control-plane.yaml` back, commit, push, let the sync run. Never `kubectl set image` — a GitOps controller reverts it within minutes, so the rollback appears to work and then undoes itself. |
| A bad policy on a phone | Fix it in the console. The device re-syncs on the next event or within the heartbeat interval; clears run before adds, so a partial apply leaves the phone *less* restrained, never more. |
| The control plane is down and a phone must be freed | The DPC's offline recovery code, shown once in the console per device. |
| A phone was revoked by a mis-tapped setup code | Re-link it — console → Replace phone → type the code into the phone's recovery screen. No factory reset, same device row. See *When a phone stops reporting*, above. |
| Everything is wrong | Factory-reset the phone. This always works — `no_factory_reset` is in `FORBIDDEN_RESTRICTIONS`, never set, and cleared on every sync **and every boot** if anything else set it. Measured on a device, not argued: `FactoryResetRecoveryTest` sets the restriction on purpose, watches the platform report it, and watches the DPC take it back off (§11.12). |

The last row is the reason the third and second exist rather than being the only options. It is the
one guarantee that is worth more than any feature in this system.

---

## Decommissioning

Deleting an ArgoCD `Application` prunes **nothing**. Objects survive the Application that created
them — including a Secret holding live credentials — for as long as nobody looks, because the
controller stops tracking them rather than removing them. To remove this app: delete the manifests
from git, let the sync prune, then list what is actually left in the namespace before deleting the
namespace itself. The `hostPath` directories on the node are touched by none of that and must be
removed by hand; one of them is the database.
