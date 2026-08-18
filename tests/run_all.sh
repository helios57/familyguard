#!/usr/bin/env bash
#
# Every test layer of FamilyGuard, in one place, with one honest exit status.
#
#   0  every layer that was asked for ran and passed
#   1  a layer ran and failed
#   2  a layer could not be run at all — NOT a pass, and never reported as one
#
# The three-valued status is the whole point. A suite that silently skips the layer it cannot reach
# and exits 0 is indistinguishable from a suite that proved something, and the skipped layer is
# always the one nobody looks at again. So a missing emulator, an absent Gradle project or a Docker
# daemon that is not running each end in 2 with the reason printed next to the layer.
#
# Usage:
#   tests/run_all.sh                       # every layer
#   tests/run_all.sh backend e2e           # only the named layers
#   tests/run_all.sh --list                # what the layer names are
#
# Naming the layers is not a way to make the run green: an explicitly requested layer that cannot
# run still reports 2. It exists so the summary can print WHICH layers defined the result — the
# scope of a sweep is where its blind spot lives, and a status of 0 means nothing until you know
# what it covered.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND="$ROOT/backend"
E2E="$ROOT/tests/e2e/run.sh"
ANDROID="$ROOT/android-dpc"
GRADLEW="$ANDROID/gradlew"
INSTRUMENTED="$ROOT/tests/android/instrumented.sh"
IMAGE_SMOKE="$ROOT/tests/image/smoke.sh"

ALL_LAYERS=(secret-scan backend image e2e android-unit android-instrumented)

usage() {
	printf 'layers: %s\n' "${ALL_LAYERS[*]}"
	printf 'usage: tests/run_all.sh [layer...]\n'
}

if [ "${1:-}" = "--list" ] || [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	usage
	exit 0
fi

REQUESTED=("$@")
if [ ${#REQUESTED[@]} -eq 0 ]; then
	REQUESTED=("${ALL_LAYERS[@]}")
fi
for want in "${REQUESTED[@]}"; do
	found=no
	for known in "${ALL_LAYERS[@]}"; do
		[ "$want" = "$known" ] && found=yes
	done
	if [ "$found" = no ]; then
		printf 'unknown layer %q\n' "$want" >&2
		usage >&2
		exit 2
	fi
done

wants() {
	for want in "${REQUESTED[@]}"; do
		[ "$want" = "$1" ] && return 0
	done
	return 1
}

# Results are collected rather than exited on: a failing backend must not hide whether the e2e
# suite could even have run. The summary at the end is the report.
declare -a NAMES=() STATES=() NOTES=()

record() { # record <layer> <PASS|FAIL|NOT MEASURED> <note>
	NAMES+=("$1")
	STATES+=("$2")
	NOTES+=("$3")
	printf '\n==> %-22s %s%s%s\n' "$1" "$2" "${3:+ — }" "${3:-}"
}

section() { printf '\n---- %s ----\n' "$1"; }

# ------------------------------------------------------------ secret-scan ----
#
# First, and cheap, because it is the only layer whose finding cannot be undone after a push. Every
# other red here is a mistake you fix; a credential that reaches a remote is a credential you rotate,
# and no later commit removes it from the history somebody already cloned.
#
# CI runs the same scan on every push and is the gate. This is the pre-flight, and it is a weaker
# instrument in exactly one way: CI pins a version, and whatever is on this PATH may not be it. The
# version is printed either way, and named in the note when it differs, because a green from a
# different ruleset is a different claim.
#
# There is no allowlist: no `.gitleaksignore`, no `paths` exemption. A fingerprint is pinned to
# commit:file:rule:line and is void the moment history is rewritten; a path exemption blinds the
# scanner to a whole class of file. Where a fixture tripped the scan, the fixture was made to stop
# being secret-shaped — IMPLEMENTATION_PLAN.md 6.9.
run_secret_scan() {
	section "secret-scan: gitleaks over the full history"
	if ! command -v gitleaks >/dev/null 2>&1; then
		record secret-scan "NOT MEASURED" \
			"no gitleaks on PATH — the version CI pins is in .github/workflows/ci.yml"
		return
	fi
	if [ ! -d "$ROOT/.git" ]; then
		# `detect` over a directory that is not a repository exits 0 having read no commits.
		record secret-scan "NOT MEASURED" "$ROOT is not a git repository, so there is no history to scan"
		return
	fi
	local have want
	have="$(gitleaks version 2>/dev/null | tr -d '\r')"
	want="$(command grep -m1 'GITLEAKS_VERSION:' "$ROOT/.github/workflows/ci.yml" | tr -d ' "' | cut -d: -f2)"
	local note="gitleaks $have"
	[ "$have" = "$want" ] || note="gitleaks $have, CI pins ${want:-unknown} — CI is the gate"
	# The same flags CI uses. `--redact` matters more here than there: this output is a terminal
	# scrollback that gets pasted into issues.
	(cd "$ROOT" && gitleaks detect --source . --redact --no-banner --exit-code 1)
	case $? in
	0) record secret-scan "PASS" "$note" ;;
	1) record secret-scan "FAIL" "$note — rerun without --redact to read the report, and do not paste it" ;;
	*) record secret-scan "NOT MEASURED" "$note — gitleaks exited without completing a scan" ;;
	esac
}

# ---------------------------------------------------------------- backend ----
#
# The four commands are not interchangeable and none implies another. `go build` does not compile
# tests; neither build nor test compiles a file behind `//go:build integration`, which is why the
# tagged vet is here on its own line; and `gofmt -l` exits 0 whether or not it found anything, so
# its OUTPUT is the signal and its status is worthless.
run_backend() {
	section "backend: build, vet, vet -tags integration, test, gofmt"
	if ! command -v go >/dev/null 2>&1; then
		record backend "NOT MEASURED" "no go toolchain on PATH"
		return
	fi
	if [ ! -d "$BACKEND" ]; then
		record backend "NOT MEASURED" "no $BACKEND"
		return
	fi
	(
		cd "$BACKEND" || exit 2
		go build ./... || exit 1
		go vet ./... || exit 1
		go vet -tags integration ./... || exit 1
		go test ./... || exit 1
		dirty="$(gofmt -l .)"
		if [ -n "$dirty" ]; then
			printf 'gofmt would rewrite:\n%s\n' "$dirty"
			exit 1
		fi
	)
	case $? in
	0) record backend "PASS" "" ;;
	2) record backend "NOT MEASURED" "could not enter $BACKEND" ;;
	*) record backend "FAIL" "" ;;
	esac
}

# ------------------------------------------------------------------ image ----
#
# The layer between "the code is correct" and "the deployment works". Every Go suite above runs a
# binary the host built, as the developer, on a writable filesystem, with the host's CA bundle and
# zoneinfo — all four differ in the cluster, and each difference fails the same way: a pod that
# comes up, answers /healthz, and dies the first time it needs the missing thing.
#
# Registered only now that it has been calibrated. All twelve of its cases were run against a
# deliberately broken product; ten went red on the named assertion first time, and the two that did
# not were defects in this script rather than in the image — a crashed container reported as NOT
# MEASURED, and a container running as root reported as `ok  the running process is uid root, not
# root`, because `docker top` renders uid 0 as a name. Both are fixed and re-calibrated. Registering
# it before that would have added an uncalibrated green to the aggregate, which makes the summary
# weaker than leaving the layer out.
run_image() {
	section "image: the built container under the manifest's own restrictions"
	if [ ! -x "$IMAGE_SMOKE" ]; then
		record image "NOT MEASURED" "$IMAGE_SMOKE is missing or not executable"
		return
	fi
	# The reason a 2 carries IS the value of a 2 — "docker is not usable by this user" and "the
	# registry is out of reach" are different things to go and fix. Read from a tee'd copy so the
	# output still streams; smoke.sh prints its reason to stderr, hence the 2>&1.
	local log
	log="$(mktemp)"
	"$IMAGE_SMOKE" 2>&1 | tee "$log"
	local rc=${PIPESTATUS[0]}
	local note
	note="$(command grep -a '^NOT MEASURED: ' "$log" | tail -n1 | cut -d' ' -f3-)"
	rm -f "$log"
	case $rc in
	0) record image "PASS" "" ;;
	2) record image "NOT MEASURED" "${note:-the harness reported it could not run}" ;;
	*) record image "FAIL" "" ;;
	esac
}

# -------------------------------------------------------------------- e2e ----
run_e2e() {
	section "e2e: black-box suite against a real server and a real PostgreSQL"
	if [ ! -x "$E2E" ]; then
		record e2e "NOT MEASURED" "$E2E is missing or not executable"
		return
	fi
	"$E2E"
	case $? in
	0) record e2e "PASS" "" ;;
	2) record e2e "NOT MEASURED" "the harness reported it could not run (see its output above)" ;;
	*) record e2e "FAIL" "" ;;
	esac
}

# ---------------------------------------------------------------- android ----
#
# Both Android layers are 2 rather than absent while Phase 5 is unwritten. The DPC is the half of
# this system that touches the child's phone, and a green run that quietly omitted it would be the
# most misleading output this script could produce.
android_missing() {
	if [ ! -x "$GRADLEW" ]; then
		printf 'no Gradle wrapper at %s' "$GRADLEW"
		return 0
	fi
	return 1
}

# The layer produces the evidence it reports, rather than trusting Gradle's exit status.
#
# `:app:testDebugUnitTest` reported `FROM-CACHE` here — the build cache satisfied it, so no test JVM
# started — and this layer printed PASS. That much is defensible on its own: a cache hit means the
# inputs were identical. What is not defensible is what it left behind. The results directory on
# disk described **4 tests in 1 class** while a forced execution of the same task runs **31 tests in
# 6 classes**, so the only readable artifact of a green gate misdescribed the gate by a factor of
# eight, and nothing in the output said so. A reader who opens those files to answer "what did we
# actually check" is misled in the direction of comfort.
#
# So: clear the results, force the execution, then count what came back. `--rerun` costs ~3s here,
# which buys the difference between a status and a measurement.
#
# The class check is the half that keeps working as the suite grows. It compares the classes that
# REPORTED against the test classes DECLARED in `*Test.kt`, which is the failure this cannot
# otherwise see:
# a test class that stops being discovered — renamed, moved to the wrong package, its annotations
# lost in a refactor — takes its assertions out of the suite while every remaining test still passes
# and the task still exits 0. A count pinned to a number would need editing on every new test and
# would be edited to whatever the run printed; a comparison against the source tree cannot be.
run_android_unit() {
	section "android-unit: JVM unit tests (enforcement engine, usage, recovery)"
	local why
	if why="$(android_missing)"; then
		record android-unit "NOT MEASURED" "$why"
		return
	fi
	local results="$ANDROID/app/build/test-results/testDebugUnitTest"
	rm -rf "$results"
	(cd "$ANDROID" && ./gradlew --console=plain :app:testDebugUnitTest --rerun)
	local gradle_rc=$?
	local report
	report="$(unit_report "$results" "$ANDROID/app/src/test")"
	local report_rc=$?
	printf '%s\n' "$report"
	if [ "$gradle_rc" -ne 0 ]; then
		record android-unit "FAIL" ""
		return
	fi
	case $report_rc in
	0) record android-unit "PASS" "" ;;
	*) record android-unit "NOT MEASURED" "gradle exited 0 but $report" ;;
	esac
}

# Reads the JUnit XML back and answers three questions the exit status cannot: did anything run, did
# every class that exists report, and does the XML itself admit a failure. Prints one line; exits 0
# when the run is fully accounted for and 1 when it is not.
unit_report() {
	python3 - "$1" "$2" <<-'PY'
		import glob, os, re, sys, xml.etree.ElementTree as ET

		results, sources = sys.argv[1], sys.argv[2]
		files = sorted(glob.glob(os.path.join(results, "TEST-*.xml")))
		if not files:
		    print("no JUnit XML under %s: the task exited 0 having reported nothing" % results)
		    sys.exit(1)

		total = failures = errors = 0
		reported = set()
		for path in files:
		    root = ET.parse(path).getroot()
		    reported.add(root.get("name").rsplit(".", 1)[-1])
		    total += int(root.get("tests"))
		    failures += int(root.get("failures"))
		    errors += int(root.get("errors"))

		# The unit of comparison is the DECLARED CLASS, not the file name. It used to be the file
		# name, and that was wrong in both directions: `StateApplierTest.kt` declares five classes
		# and none of them is called `StateApplierTest`, so a correct suite read as silent; while
		# `AppSuspensionManagerTest.kt` declares two and only one was ever demanded, so the other
		# could have stopped reporting with the guard still green. Parsing the declarations is
		# strictly finer — 21 classes to account for rather than 18 files.
		#
		# Nested classes still report as Outer$Inner, so the match tolerates a `$` on either side.
		# Any modifier prefix is accepted except `abstract` — an abstract base legitimately never
		# reports on its own. `private` is deliberately NOT excluded: making a test class private is
		# one of the ways it stops being discovered, and a rule that skipped private classes would
		# lose sight of the class on both sides at once, which is the definition of a blind guard.
		declared = {}
		empty_files = []
		for p in sorted(glob.glob(os.path.join(sources, "**", "*Test.kt"), recursive=True)):
		    names = [
		        n
		        for mods, n in re.findall(
		            r"^[ \t]*(?:@\w+[ \t]*)*((?:\w+[ \t]+)*)class[ \t]+(\w+Test)\b",
		            open(p, encoding="utf-8").read(),
		            re.M,
		        )
		        if "abstract" not in mods.split()
		    ]
		    if not names:
		        empty_files.append(os.path.basename(p))
		    for n in names:
		        declared[n] = os.path.basename(p)

		# A `*Test.kt` that declares no test class at all is a file that measures nothing, and it
		# would otherwise be invisible here: nothing is missing from the report because nothing was
		# ever expected of it.
		if empty_files:
		    print("these *Test.kt files declare no test class: %s" % ", ".join(sorted(empty_files)))
		    sys.exit(1)

		silent = sorted(
		    "%s (%s)" % (n, f)
		    for n, f in declared.items()
		    if not any(r == n or r.startswith(n + "$") or r.endswith("$" + n) for r in reported)
		)

		if silent:
		    print("these test classes exist in src/test but reported nothing: %s" % ", ".join(silent))
		    sys.exit(1)
		if total == 0:
		    print("%d classes reported, all of them empty: 0 testcases ran" % len(reported))
		    sys.exit(1)
		if failures or errors:
		    print("%d tests, %d failures, %d errors" % (total, failures, errors))
		    sys.exit(1)
		print("%d tests in %d classes, all green" % (total, len(reported)))
	PY
}

# The device half is delegated in full. It has to provision the device (the tests measure a
# device-owned phone and fail rather than skip without one, so a plain emulator would otherwise
# report FAIL — a false red) and it has to reboot the device between two runs to produce the FR-2.3
# evidence. None of that belongs in a dispatcher, and all of it is worth running on its own.
run_android_instrumented() {
	section "android-instrumented: on-device tests (needs a running emulator or a phone; REBOOTS it)"
	local why
	if why="$(android_missing)"; then
		record android-instrumented "NOT MEASURED" "$why"
		return
	fi
	# `connectedDebugAndroidTest` over an empty androidTest source set exits 0 having installed
	# nothing and run nothing — a PASS for the one layer that touches a real phone, reported by a
	# task that never opened a test. So the sources are checked before the device is.
	if [ -z "$(find "$ANDROID/app/src/androidTest" -name '*.kt' -o -name '*.java' 2>/dev/null | head -n1)" ]; then
		record android-instrumented "NOT MEASURED" "there are no instrumented tests"
		return
	fi
	if [ ! -x "$INSTRUMENTED" ]; then
		record android-instrumented "NOT MEASURED" "$INSTRUMENTED is missing or not executable"
		return
	fi
	# The runner's own three-valued status is the answer; its last RESULT line carries the reason,
	# and the reason is the whole value of a 2. Read from a tee'd copy so the output still streams.
	local log
	log="$(mktemp)"
	"$INSTRUMENTED" 2>&1 | tee "$log"
	local rc=${PIPESTATUS[0]}
	local note
	note="$(command grep -a '^RESULT	' "$log" | tail -n1 | cut -f3-)"
	rm -f "$log"
	case $rc in
	0) record android-instrumented "PASS" "$note" ;;
	2) record android-instrumented "NOT MEASURED" "${note:-the runner reported it could not measure}" ;;
	*) record android-instrumented "FAIL" "$note" ;;
	esac
}

wants secret-scan && run_secret_scan
wants backend && run_backend
wants image && run_image
wants e2e && run_e2e
wants android-unit && run_android_unit
wants android-instrumented && run_android_instrumented

# ---------------------------------------------------------------- summary ----
printf '\n================ summary ================\n'
printf 'layers requested: %s\n\n' "${REQUESTED[*]}"
rc=0
for i in "${!NAMES[@]}"; do
	printf '  %-22s %-13s %s\n' "${NAMES[$i]}" "${STATES[$i]}" "${NOTES[$i]}"
	case "${STATES[$i]}" in
	FAIL) rc=1 ;;
	"NOT MEASURED") [ "$rc" -eq 0 ] && rc=2 ;;
	esac
done
printf '\n'
case $rc in
0) printf 'every requested layer ran and passed\n' ;;
1) printf 'a layer FAILED\n' ;;
2) printf 'a layer could not be run — this is NOT a pass\n' ;;
esac
exit $rc
