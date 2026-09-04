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

Two `hostPath`s rather than PersistentVolumeClaims, because the reference target is a single node and
a `hostPath` is an honest description of what a single-node volume is. On a cluster with more than
one node, replace both with PVCs; that is the one substitution in `deploy/` that is not a string.

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
ssh k8s-node 'sudo install -m 0644 /tmp/familyguard.apk /tmp/familyguard.der \
             /srv/familyguard/apk/ && rm /tmp/familyguard.apk /tmp/familyguard.der'
```

Restart the control plane afterwards so it re-reads the files and recomputes both checksums — the
values are computed once at startup:

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

Enrolled devices are **not** re-enrolled. They are updated from the console: open the device and send
**Update app**, and the phone downloads what this server now hosts and installs it over itself
(FR-15). It is an ordinary instant command, so it queues while the phone is off and runs when it
comes back.

Two things about that are worth knowing before you use it. The phone refuses anything that is not
strictly newer, not the same package, not signed by the same key, or that does not match the
checksum this server published — so dropping in an APK signed with a different key does not produce
a broken fleet, it produces a refusal with a reason under the command. And the acknowledgement
arrives *before* the install, because the install kills the process that would otherwise send it:
what tells you the update landed is the build number the device reports on its next heartbeat, shown
on the device page. A phone that acknowledged and never reported a new build is a phone to look at.

Signing a new APK with a **different** key breaks provisioning for new devices and cannot upgrade
existing ones at all. There is no recovery from a lost signing key other than factory-resetting every
enrolled phone — which the fleet permits, by design, because `no_factory_reset` is never set.

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
| `GET_USAGE_STATS` (`PACKAGE_USAGE_STATS`) | **Screen time cannot be measured at all.** Every query returns nothing. The DPC reports *not measured* rather than zero, so the console says so instead of showing a child who spent the day off their phone — but the daily limit (FR-3) can never be reached until this is granted. |
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

---

## Rollback

| Situation | What to do |
|---|---|
| A bad image version | Edit the tag in `deploy/control-plane.yaml` back, commit, push, let the sync run. Never `kubectl set image` — a GitOps controller reverts it within minutes, so the rollback appears to work and then undoes itself. |
| A bad policy on a phone | Fix it in the console. The device re-syncs on the next event or within the heartbeat interval; clears run before adds, so a partial apply leaves the phone *less* restrained, never more. |
| The control plane is down and a phone must be freed | The DPC's offline recovery code, shown once in the console per device. |
| Everything is wrong | Factory-reset the phone. This always works — `no_factory_reset` is in `FORBIDDEN_RESTRICTIONS`, never set, and cleared on every sync if anything else set it. |

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
