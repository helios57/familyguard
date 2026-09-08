# The control plane, as one static binary in an image with no shell.
#
# Build context is this directory (`backend/`), so nothing outside the Go module can be copied into
# the image by accident — not the Android sources, not the tests, and not a stray `.env` a developer
# left in the working tree.
#
#   docker build -t familyguard-control-plane:dev backend/
#
# The final stage is `distroless/static`, which contains the binary, CA certificates, /etc/passwd and
# nothing else: no shell, no package manager, no busybox. That is not decoration. This server holds
# a session signing key and a database password in its environment, and the usual next step after an
# RCE is to run something — so there is nothing to run.

FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first, as their own layer: `go.mod`/`go.sum` change far less often than the code, so
# an ordinary source edit does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off is what makes the binary run in a `static` image at all — with cgo the net and os/user
# packages link against glibc, which is not there. `-trimpath` keeps the build machine's directory
# layout out of the binary, and the version is stamped in rather than hardcoded so `--version` in a
# running pod can be compared against the tag that was deployed.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/server ./cmd/server

# Two properties the next stage depends on, checked here rather than discovered as a
# CrashLoopBackOff:
#
#   - the binary is really cgo-free. Read from the binary's own build settings, not from the
#     environment variable that was *meant* to produce them — `go build` silently ignores
#     CGO_ENABLED when a directive re-enables it.
#   - it runs, and refuses an empty configuration. This is the one command that proves both: a
#     dynamically linked binary in a static image, or a corrupted one, prints something else
#     entirely, so the absence of this exact line is a failure either way. The polarity is the
#     interesting half — grep's status is the step's status, so a server that started *successfully*
#     with no DATABASE_URL fails the build too.
RUN go version -m /out/server | grep -q 'CGO_ENABLED=0'
RUN /out/server 2>&1 | grep -q 'DATABASE_URL is required'

# The CLI, cross-compiled for the platforms a parent might run it on, and served by this same
# server at /fgctl. Baked into the image rather than mounted from the node so that the binaries and
# the server are one artefact: they are stamped with the same VERSION above, which is what lets
# `fgctl self-update` answer "am I current?" by comparing one number instead of two that can drift.
#
# The cost is honest and worth stating: six static Go binaries add roughly 55 MB to an image that is
# otherwise about 30 MB. That is one pull per release, and the alternative -- a second out-of-band
# upload step per platform, like the DPC has -- is the thing that made the DPC's version
# unanswerable from the server in the first place.
#
# `if`, not `[ "$os" = windows ] && ext=.exe`: under `set -e` the && form exits non-zero on every
# non-windows target and aborts the loop at the first one.
RUN set -eu; \
    for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do \
        os="${target%/*}"; arch="${target#*/}"; ext=""; \
        if [ "$os" = windows ]; then ext=".exe"; fi; \
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
            -trimpath \
            -ldflags="-s -w -X main.version=${VERSION}" \
            -o "/out/fgctl/fgctl-$os-$arch$ext" ./cmd/fgctl; \
    done

# Two checks, for the same reason the server has them: a missing target here becomes a 404 on a
# download link, which is discovered by a parent rather than by this build.
#
#   - all six exist. `go build` failing would already have stopped the loop, but a typo in the -o
#     path would not, and would silently produce five files and one oddly-named seventh.
#   - the CLI runs, and reports the version that was stamped into it. This is also the only check
#     that the ldflags path is right: a mistyped -X leaves `version` at "dev", the binary works
#     perfectly, and self-update then compares "dev" against the server forever.
RUN [ "$(ls -1 /out/fgctl | wc -l)" -eq 6 ] || { echo "expected 6 fgctl builds, got:"; ls -l /out/fgctl; exit 1; }
RUN /out/fgctl/fgctl-linux-amd64 version | grep -q "${VERSION}"

# Pinned by digest, not by `:nonroot`. A tag is a pointer the publisher can move, so two builds of
# the same commit can produce two different images — and the one property this stage is chosen for
# (nothing to execute) is a property of the *content*, which a tag does not name. Verified by digest
# is also the only way to answer "is what is running what we built" from the cluster.
#
# This is the multi-arch *index* digest, not a per-platform manifest: `docker manifest inspect` on
# it returns `application/vnd.oci.image.index.v1+json` with six children (linux on amd64, arm64,
# arm, s390x, ppc64le, riscv64). That distinction is load-bearing — pinning a per-platform digest here would
# build only on that architecture and fail everywhere else with a confusing manifest error. At the
# time of pinning, `:nonroot` resolved to exactly these six children.
#
# Bumping it: `docker pull gcr.io/distroless/static-debian13:nonroot` then
# `docker inspect -f '{{index .RepoDigests 0}}' gcr.io/distroless/static-debian13:nonroot`.
# `tests/image/smoke.sh` reads this line rather than repeating it, so there is nothing else to edit.
FROM gcr.io/distroless/static-debian13@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

# `nonroot` is uid 65532 in the base image. Naming it numerically as well is what lets Kubernetes'
# `runAsNonRoot` admission check pass: it compares a number, and refuses to start a container whose
# user is only a name it cannot resolve.
USER 65532:65532

COPY --from=build /out/server /server

# Served at /fgctl by the server above; FGCTL_DIR defaults to this path.
COPY --from=build /out/fgctl /fgctl

# Documentation only — the port comes from ADDR, and the manifest sets both from one value.
EXPOSE 8080

# No shell form: `ENTRYPOINT /server` would need `/bin/sh -c`, which does not exist here, and the
# container would fail to start with an error that names the binary rather than the missing shell.
# Exec form also makes the process pid 1, so the SIGTERM Kubernetes sends reaches the graceful
# shutdown in main.go instead of a shell that ignores it.
ENTRYPOINT ["/server"]
