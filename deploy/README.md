# Deploying FamilyGuard — a worked example

Everything needed to stand the whole system up, in six files:

| File | |
|---|---|
| `namespace.yaml` | `familyguard`, sharing nothing with the rest of the cluster |
| `postgres.yaml` | the database, its Service and its volume |
| `control-plane.yaml` | the server, its Service, and every environment variable it reads |
| `ingress.yaml` | the hostname and its TLS |
| `kustomization.yaml` | the four above, in order |
| `secret.example.yaml` | **not** in the kustomization — the contract for the one Secret, and a warning |

Applying it is one command. Making it *yours* is five edits, listed below.

This directory is an **example**, in the strict sense: it is complete and it works, and it is not
what any particular cluster runs. It is what NFR-11 asks for — the whole system deployed from git,
with no manual `kubectl` step that a later sync would erase. A real deployment keeps its own
hostname, storage paths, image tag and secret plumbing in its own repository and shares only the
shape. Every site-specific value here is spelled `example.com`, `/srv/familyguard` or `REPLACE_ME`,
so a value that still says `example` after you have adapted it is a value you have not adapted.

```bash
kubectl kustomize deploy     # render and read it first
kubectl apply -k deploy      # …then apply
```

## The five edits

| # | Where | What |
|---|---|---|
| 1 | `ingress.yaml`, `control-plane.yaml` | `guard.example.com` → your hostname, in **four** places: the Ingress `tls.hosts` and `rules.host`, and the control plane's `PUBLIC_URL` and `APK_URL`. They must agree; the QR payload and the OAuth redirect are both built from `PUBLIC_URL`. |
| 2 | `postgres.yaml`, `control-plane.yaml` | `/srv/familyguard/...` → your storage. Two `hostPath` volumes, or a PVC each if your cluster has a StorageClass. |
| 3 | `control-plane.yaml` | `BOOTSTRAP_PARENT_EMAILS` → the Google account that may sign in before any parent exists. |
| 4 | `control-plane.yaml` | `TRUSTED_PROXIES` → what your ingress actually presents. Read the comment there; both directions of wrong are silent. |
| 5 | — | the `familyguard-secret` Secret. See `secret.example.yaml`, which is a contract and a warning, not a file to apply. |

## Three things that must be true before the first apply

None of them is something a sync can do for itself, and each fails in a way that looks like
something else.

1. **DNS resolves your hostname to the ingress**, and cert-manager can complete an HTTP-01
   challenge for it. Without this you get a certificate that never issues and an Ingress that
   serves the default backend — which reads as "the app is broken", not "DNS is missing".

2. **The postgres directory exists and is owned by uid 50140**, if you kept the `hostPath`. The pod
   runs unprivileged and cannot chown its own data directory; there is deliberately no root init
   container to do it for you. `initdb` fails on permissions and the pod crash-loops.

3. **The signed APK and its DER certificate are in the APK directory**, named `familyguard.apk` and
   `familyguard.der`. The control plane refuses to start without them — deliberately, because the
   alternative is a console whose "Setup QR" button 500s in front of a parent holding a
   factory-reset phone. Building and signing them is in [../DEPLOYMENT.md](../DEPLOYMENT.md).

## What this example leaves to you

- **GitOps.** These are plain manifests. Point Argo CD, Flux or `kubectl apply -k` at them; nothing
  here depends on which.
- **Secret management.** The manifests name a Secret and read five keys from it. How it is
  materialised is yours — see `secret.example.yaml`.
- **Pod restart on rotation.** The Deployments carry `reloader.stakater.com/auto`, which does
  nothing unless [Stakater Reloader](https://github.com/stakater/Reloader) is installed. Without it,
  rotating a secret needs `kubectl rollout restart`. A secrets operator updating a Secret does not
  restart anything by itself, and a pod holding a revoked signing key in memory keeps honouring
  cookies you believe you killed.
- **Backups.** There are none here. The database holds every device, policy, enrollment and command
  history; the `hostPath` is not a backup and neither is a single node.
- **`ingressClassName: nginx` and the `nginx.ingress.kubernetes.io/*` annotations.** Three of them
  are load-bearing — body size for the APK download, and read-timeout plus buffering-off for the two
  SSE streams. A different ingress controller needs its own spelling of all three, and the failures
  are quiet: a truncated download, and a phone that merely seems slow to react.
