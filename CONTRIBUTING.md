# Contributing

This is a small project with an opinionated bar for evidence. The bar is the interesting part, so
it is stated first; everything below it is mechanics.

---

## The one rule

**A test that has never failed has not been shown to work** (NFR-12).

Every guard in this repository was broken deliberately, observed red, and restored. That is not
ceremony — it is the only thing separating a control from a comment. This codebase has produced,
and caught, all of the following:

- a uid check that read `docker top`, resolved uid 0 through the *host's* passwd file, and printed
  `ok  the running process is uid root, not root` for a container running as root;
- a citation scanner written with a backspace escape instead of a word boundary — a pattern that
  matches nothing, in a program whose entire job is to match — reporting the repository clean;
- a crash under `--read-only` reported as *not measured*, because the container exited before Docker
  could name a port, so the script bailed one line before the assertion that existed to catch it.

None of the three had a symptom. All three were green. So when you add a check, break the thing it
checks, watch it go red, put it back, and say so in the commit message.

Two corollaries:

- **A negative control that stays green is information, not a pass.** It means your test does not
  bind to what you changed. Say that, rather than counting it as evidence.
- **A layer that cannot run reports 2, never 0.** `tests/run_all.sh` is three-valued for this
  reason: 0 ran and passed, 1 ran and failed, 2 could not run. A suite that silently skips the layer
  it cannot reach is indistinguishable from one that proved something.

---

## Running the tests

```bash
tests/run_all.sh --list      # the layer names
tests/run_all.sh             # every layer
tests/run_all.sh backend e2e # only these two
```

| Layer | What it needs | What it covers |
|---|---|---|
| `secret-scan` | `gitleaks` on `PATH`, in a git checkout | the full commit history, redacted |
| `backend` | Go toolchain | build, vet, `vet -tags integration`, test, `gofmt` |
| `image` | Docker | twelve properties of the built container, under the manifest's own restrictions |
| `e2e` | Docker | black box: a real server binary, a real PostgreSQL, a real browser |
| `android-unit` | JDK 21 and the Android SDK | the DPC's JVM suite, plus the two repository-wide guards: requirement citations and documentation links |
| `android-instrumented` | an emulator promoted to Device Owner | provisioning, suspension, DNS policy, commands, across a real reboot |

Naming layers on the command line does not make a run green: a layer you asked for that cannot run
still exits 2. It exists so the summary can print *which* layers produced the result — the scope of a
sweep is where its blind spot lives, and an exit status of 0 means nothing until you know what it
covered.

CI (`.github/workflows/ci.yml`) runs the first five. The instrumented layer needs hardware nobody
gives a runner for free, so it is a local gate; if your change touches the DPC's device-facing code,
run it and say what you saw.

---

## Requirements and citations

[`REQUIREMENTS.md`](REQUIREMENTS.md) is the authority. Every `FR-…` and `NFR-…` written anywhere in
this repository must resolve to a requirement in it, and every requirement must be named by
something. `RequirementCitationsTest` enforces both directions and runs in the `android-unit` layer,
scanning every language.

Both directions matter, and the reason is not symmetry:

- A citation pointing at a number nobody wrote reads exactly like one pointing at the right
  requirement. It is *worse* than no citation, because a reviewer follows it to decide whether the
  code does what was asked, and lands on a requirement that says something else. Four such citations
  existed before that test did.
- A requirement nothing names is either dropped without being withdrawn, or implemented by code
  citing the wrong number.

So: changing a requirement's id is a repository-wide edit, and adding a requirement means adding the
thing that claims it. If a requirement is genuinely withdrawn, delete it — do not leave it in the
document with nothing behind it.

`DocumentationLinksTest` is the same guard pointed at the other kind of reference. The eight Markdown
documents link to each other about fifty times, and a link to a renamed file or a renamed heading
fails the way GitHub fails it: silently, by landing the reader at the top of the page. So renaming a
document or a heading is also a repository-wide edit. The anchor rule the guard implements is
GitHub's, and the naive version of it is wrong — GitHub maps *each* space to its own hyphen and does
not collapse runs, so `## 6.6 — the row` is `#66--the-row`, double hyphen and all. That case is
pinned in the test; a "tidier" slug function turns every such link red.

Both guards run in the `android-unit` layer and scan the repository from its root, which means
`tests/run_all.sh` invokes the Gradle task with `--rerun`. Without it, editing only Markdown leaves
the test task **up to date** and Gradle skips it — a guard whose inputs Gradle cannot see is a guard
that stops running exactly when you change what it watches.

---

## Secrets

CI scans the **full commit history** with gitleaks on every push — a scan of the current tree would
clear a credential by deleting the file that carried it, which changes nothing for anyone who already
cloned. This repository has **no allowlist** —
no `.gitleaksignore`, no `paths` exemption. Both are tempting and both are traps. An ignore entry is
pinned to `commit:file:rule:line`, so it is void the moment history is rewritten and decays into a
red nobody can reproduce; a `paths` exemption over `*_test.go` clears one finding by blinding the
scanner to every credential anyone ever pastes into a test.

When a fixture trips the scanner, make the fixture stop being secret-shaped. A test that asserts a
token's *length* does not need a token's *entropy* — build the value from a repeated string rather
than writing out something that looks like a credential.

If you calibrate the scanner — and you should, before believing a green — **do not use an example
credential from documentation.** `AKIAIOSFODNN7EXAMPLE` and its friends are allowlisted inside
gitleaks itself, so planting one reports *no leaks found* on a file the scanner definitely read, and
the obvious conclusions are that the scanner is broken or that the tree is clean. Neither is true.
Build the probe from the *shape* — the right prefix and the right length, characters of your own —
and delete it before you commit. IMPLEMENTATION_PLAN.md 6.14 is the record.

Never paste a secret into an issue, a PR, a commit message or a log, not even a prefix. See
[SECURITY.md](SECURITY.md).

---

## Style

**Go.** `gofmt` is a gate, and it fails in a way worth knowing about: `gofmt -l` exits **0** whether
or not it found anything, so the *output* is the signal and never the status. Deleting one entry
from an aligned map literal re-aligns every sibling, which means a pure removal is exactly the change
most likely to trip it.

```bash
cd backend
go build ./... && go vet ./... && go vet -tags integration ./... && go test ./...
d=$(gofmt -l .); [ -z "$d" ] || { echo "GOFMT DIRTY:"; echo "$d"; }
```

`go vet -tags integration` is not covered by the other three. A `//go:build integration` file is not
compiled without the tag, so a type error inside one is invisible to a fully green local sweep and
lands in CI instead.

**Kotlin.** `./gradlew :app:assembleDebug :app:testDebugUnitTest` from `android-dpc/`. Built with
JDK 21 — the version CI pins — and compiled to JVM 17 bytecode; `allWarningsAsErrors` is on, so a
deprecation is a build failure rather than a line of scrollback. The DPC targets `minSdk 29`; an API
newer than that needs a version guard and a note saying what happens on 29.

**Comments explain why, not what.** The valuable comments in this codebase are the ones recording a
thing that was tried and failed — a restriction key the platform silently ignores, an appop no
device-owner API can grant, a probe that answered about the wrong server. Those do not survive in
code, and re-deriving them costs a day each.

---

## Commits and pull requests

Say **why**, and say what you disproved. A commit message that records "I expected X, measured Y" is
worth more than one describing the diff, which git already has.

Include, where it applies:

- what you calibrated, and what went red;
- what you did **not** verify, named as such rather than omitted;
- whether a green you are reporting came from a layer that actually ran.

CI is the gate. A local green is a prediction — a useful one, but the workflow is what decides.

---

## Deploying

Don't, from a pull request. [`deploy/`](deploy/) is a worked example that renders as it stands, and
[`DEPLOYMENT.md`](DEPLOYMENT.md) is the runbook for a first apply. Changes to either are reviewed as
documentation: they cannot be verified by running them here, so they are held to the standard of
saying what each step's absence looks like.
