package e2e

// FR-15 on a real Android device: the control plane replaces the DPC on a phone that is already
// enrolled, without anybody touching the phone.
//
// Everything else in this suite is the server's half. This file is the only place where the two
// halves meet, and it is the only evidence that the feature works at all — the JVM tests prove the
// updater's five checks, the e2e tests prove the endpoints, and neither of them can install
// anything. What is unique here is the part that cannot be faked: a PackageInstaller session
// committed by a device owner, a process killed by its own install, and a foreground service that
// has to come back on `MY_PACKAGE_REPLACED` or the phone is silently unmanaged from then on.
//
// It is driven by tests/android/self-update.sh, which builds the two APKs, installs the first one
// and makes it the device owner. Run on its own — by `go test ./...`, or by tests/e2e/run.sh — it
// SKIPS, because there is no device: a skip is honest here only because the script that provides
// the device also insists that this test ran, and reports NOT MEASURED if it did not.

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	dpcPackage = "io.github.helios57.familyguard"
	// AGP derives the instrumentation package from the applicationId. Spelled out rather than
	// derived, for the same reason the provisioning extras are: a rename on the Android side would
	// otherwise make this test look for a runner that no longer exists and report it as a device
	// problem.
	dpcTestRunner = dpcPackage + ".test/androidx.test.runner.AndroidJUnitRunner"
	enrollClass   = dpcPackage + ".ServerDrivenEnrollmentTest"

	// The instrumentation arguments ServerDrivenEnrollmentTest reads.
	argServerURL       = "familyguardServerUrl"
	argEnrollmentToken = "familyguardEnrollmentToken"

	// The address an Android emulator has for the machine it runs on. Fixed by the emulator's NAT,
	// and it maps to the host's *loopback* — so the harness goes on listening on 127.0.0.1 and the
	// phone reaches that same socket by this name.
	//
	// This is not a convenience. The first policy the DPC applies contains `no_debugging_features`,
	// which switches adb off, and an `adb reverse` tunnel does not survive it: measured three times
	// on 2026-09-04, each run dying at `DevicePolicyManager: Changing user restriction
	// no_debugging_features on user 0 to: true` with the phone's next heartbeat refused on a
	// loopback port that no longer forwarded anywhere. The product was behaving correctly and the
	// test had wired the device's only route to the control plane through the thing the product
	// turns off. A route the DPC cannot revoke is the fix.
	emulatorHostAlias = "10.0.2.2"

	// How long a queued command may take to reach the phone, install, and be reported back.
	//
	// This test queues the command in a gap it cannot avoid: it waits for the heartbeat that reports
	// the running build, and the DPC opens its event stream a beat AFTER that heartbeat, once its
	// start-of-session sync has finished uploading the app inventory. Measured 2026-09-04: heartbeat
	// 23:39:29.13, this test's command 23:39:29.28, inventory 23:39:30.54. The command was therefore
	// published to a device with no stream open — and a publish reaches the streams that are open at
	// that instant and nothing else.
	//
	// That used to mean the command was simply lost: the DPC's five-minute poll re-enforces from
	// cache and fetches nothing, so run 6 sat through two polls and eleven minutes with the phone
	// healthy and the command undelivered. The fix is in the product, not in this number — the
	// stream's `connected` frame now wakes a sync, so the command is found within a second of the
	// stream opening. What is left for this deadline to cover is the work: a 16 MB download over the
	// emulator's NAT, the install, the process the install kills coming back on MY_PACKAGE_REPLACED,
	// and its first heartbeat.
	updateDeadline = 5 * time.Minute
)

// androidDevice is the emulator, and the small set of adb calls this test is allowed to make.
type androidDevice struct {
	t      *testing.T
	adb    string
	serial string

	// The build numbers of the two APKs, read from the files themselves by the driving script.
	// Held here so every assertion below names a number that came from an APK rather than from a
	// constant in this file, which would still pass if the build produced two identical APKs.
	currentBuild int64
	nextBuild    int64
}

// androidFromEnv reads what tests/android/self-update.sh exports, or skips.
func androidFromEnv(t *testing.T) *androidDevice {
	t.Helper()
	if os.Getenv("E2E_ANDROID") != "1" {
		t.Skip("no device: this test is driven by tests/android/self-update.sh, which builds two " +
			"APKs, installs the first and makes it the device owner. E2E_ANDROID=1 is how that " +
			"script says it has done so.")
	}
	d := &androidDevice{
		t:      t,
		adb:    os.Getenv("E2E_ANDROID_ADB"),
		serial: os.Getenv("E2E_ANDROID_SERIAL"),
	}
	if d.adb == "" {
		t.Fatal("E2E_ANDROID=1 without E2E_ANDROID_ADB; nothing here can reach the device")
	}
	d.currentBuild = mustBuildNumber(t, "E2E_ANDROID_CURRENT_BUILD")
	d.nextBuild = mustBuildNumber(t, "E2E_ANDROID_NEXT_BUILD")
	// The check that stops this test being vacuous. Two APKs built from the same tree differ only
	// because `-PbuildOffset` was passed; a property that silently did nothing would produce two
	// identical builds, the updater would correctly refuse to install one over the other, and a
	// test that only looked for "the version changed" would have nothing to say about why it did
	// not. The driving script asserts this too — it is cheap, and this is the assertion that gets
	// read when the test fails.
	if d.nextBuild <= d.currentBuild {
		t.Fatalf("the APK to install is build %d and the phone already has build %d; FR-15.3 installs "+
			"on a strictly greater build, so there is nothing here to prove", d.nextBuild, d.currentBuild)
	}
	return d
}

func mustBuildNumber(t *testing.T, key string) int64 {
	t.Helper()
	raw := os.Getenv(key)
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("%s = %q, which is not a build number", key, raw)
	}
	return n
}

// run executes one adb command and returns its combined output.
//
// Combined, because adb reports a device that has gone away on stderr and a package that is not
// installed on stdout, and a helper that dropped one of them would turn one of those into an empty
// string that reads like a clean answer.
func (d *androidDevice) run(timeout time.Duration, args ...string) (string, error) {
	d.t.Helper()
	full := args
	if d.serial != "" {
		full = append([]string{"-s", d.serial}, args...)
	}
	cmd := exec.Command(d.adb, full...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return string(out), fmt.Errorf("adb %s did not return within %s", strings.Join(args, " "), timeout)
	}
	return string(out), err
}

func (d *androidDevice) mustRun(timeout time.Duration, args ...string) string {
	d.t.Helper()
	out, err := d.run(timeout, args...)
	if err != nil {
		d.t.Fatalf("adb %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// dumpDeviceLogOnFailure registers a cleanup that prints what the PHONE said, and only when this
// test has failed.
//
// Every assertion in this file is a sentence about the server's view — "the console still shows the
// phone running build 0" — and the cause is always on the device. Without this the red is
// unfalsifiable from the test log: a DPC that crashed applying its first policy, one whose service
// was never started, and one whose heartbeat was refused all produce that identical line. Measured
// twice, on 2026-09-04: two runs failed with exactly that message and neither log said anything
// more, and the diagnosis had to come from the emulator's own logcat capture afterwards.
//
// Three things are captured rather than one, because each answers a different question the failure
// leaves open: the DPC's own log lines say what it decided, `AndroidRuntime:E` carries the stack of
// an uncaught exception that killed it, and the service dump says whether the thing that does the
// syncing is running at all.
//
// `logcat -d` reads the *current* buffer, which the platform truncates on rotation — so it can
// return zero bytes with status 0, which is byte-identical to a device that said nothing. The byte
// count is therefore reported, and an empty capture says so in words rather than printing nothing.
func (d *androidDevice) dumpDeviceLogOnFailure() {
	d.t.Cleanup(func() {
		if !d.t.Failed() {
			return
		}
		log, err := d.run(60*time.Second, "logcat", "-d", "-v", "time",
			"FamilyGuard/Connection:V", "FamilyGuard/Compliance:V", "FamilyGuard/Usage:V",
			"FamilyGuard/Admin:V", "FamilyGuardUpdate:V", "EncryptedPreferences:V",
			"AndroidRuntime:E", "*:S")
		if err != nil {
			d.t.Logf("the phone's log could not be read (%v), so this failure has no device-side "+
				"evidence at all:\n%s", err, tail(log, 1000))
			return
		}
		if strings.TrimSpace(log) == "" {
			d.t.Logf("the phone's log came back EMPTY (%d bytes). logcat -d reads the current "+
				"buffer and the platform truncates it on rotation, so this is not evidence that "+
				"the DPC was silent — it is no measurement.", len(log))
		} else {
			d.t.Logf("what the phone said (%d bytes, tail):\n%s", len(log), tail(log, 12000))
		}
		services, err := d.run(60*time.Second, "shell", "dumpsys", "activity", "services", dpcPackage)
		if err != nil {
			d.t.Logf("the DPC's services could not be dumped: %v", err)
			return
		}
		d.t.Logf("the DPC's running services:\n%s", tail(services, 3000))
	})
}

var versionCodePattern = regexp.MustCompile(`versionCode=(\d+)`)

// installedBuild is the build number the platform says is installed, which is a different authority
// from the heartbeat: the heartbeat is the app's claim about itself, and after a self-update the two
// agreeing is most of the point.
//
// `dumpsys package` prints versionCode in more than one section. Every occurrence has to agree —
// taking the first would quietly read a stale one out of, say, a hidden system package, and the
// number this test turns on would be a number nobody chose.
func (d *androidDevice) installedBuild() int64 {
	d.t.Helper()
	n, err := d.readInstalledBuild()
	if err != nil {
		d.t.Fatalf("%v", err)
	}
	return n
}

// readInstalledBuild is the same read, returning its failure instead of ending the test — because
// after the DPC's first sync there is no adb to read it over. See confirmInstalledBuild.
func (d *androidDevice) readInstalledBuild() (int64, error) {
	d.t.Helper()
	out, err := d.run(30*time.Second, "shell", "dumpsys", "package", dpcPackage)
	if err != nil {
		return 0, fmt.Errorf("dumpsys package %s could not be run: %v\n%s", dpcPackage, err, tail(out, 600))
	}
	matches := versionCodePattern.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("dumpsys package %s reports no versionCode at all; the app is not "+
			"installed, or dumpsys returned nothing:\n%s", dpcPackage, tail(out, 2000))
	}
	seen := map[string]bool{}
	for _, m := range matches {
		seen[m[1]] = true
	}
	if len(seen) != 1 {
		return 0, fmt.Errorf("dumpsys package %s reports %d different versionCodes (%v); this test "+
			"cannot say which build is running", dpcPackage, len(seen), keys(seen))
	}
	n, err := strconv.ParseInt(matches[0][1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("versionCode %q is not a number", matches[0][1])
	}
	return n, nil
}

// confirmInstalledBuild asks the platform what it installed, when there is still a way to ask.
//
// This is the one assertion in this test that is allowed to be unavailable, and the reason is the
// product working: the policy the DPC applies sets `no_debugging_features`, so from the first sync
// onwards the phone has no debug bridge. Everything after that point is read from the control
// plane instead — which is not a downgrade to hearsay, because the version the DPC reports is read
// from the package manager (see Synchronizer.DeviceTelemetry) by a process that only exists if the
// install succeeded and MY_PACKAGE_REPLACED restarted it.
//
// It is attempted rather than deleted because on a device where adb does survive — a phone whose
// restriction a parent cleared, a future policy that does not set it — it is the strongest evidence
// available, and an assertion that is silently dropped is one nobody notices has stopped running.
// When it cannot run it says NOT MEASURED, in those words, and never passes quietly.
func (d *androidDevice) confirmInstalledBuild(want int64, within time.Duration) {
	d.t.Helper()
	deadline := time.Now().Add(within)
	for {
		got, err := d.readInstalledBuild()
		if err != nil {
			d.t.Logf("NOT MEASURED: the platform's own record of the installed build could not be "+
				"read, which is expected once the DPC has applied a policy carrying "+
				"no_debugging_features — adb is off. The control plane's view is the authority "+
				"below. The read failed with: %v", err)
			return
		}
		if got == want {
			d.t.Logf("the platform reports build %d installed", got)
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("the platform still reports build %d after %s; it was told to install build %d",
				got, within, want)
		}
		time.Sleep(2 * time.Second)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// reboot restarts the phone and comes back when it has booted.
//
// **This is an artifact of the instrumentation, not of the product.** `am instrument -w` kills the
// process it ran in when it finishes, and ConnectionService lives in that process — measured: the
// enrollment lands, the phone fetches its policy once, and then nothing, because the service died
// with the test runner. On a real phone the service is started by PolicyComplianceActivity at the
// end of provisioning and nothing kills it. A reboot is how this test gets the DPC running the way
// a provisioned phone runs it, and it is the only mechanism available: ConnectionService is not
// exported (correctly), so `am start-foreground-service` answers *"Requires permission not exported
// from uid"*, and BOOT_COMPLETED is a protected broadcast `adb shell am broadcast` may not send.
//
// It also earns its cost: what starts the DPC here is BootReceiver reading the stored credential,
// so everything below is measured on a phone that came up cold, which is the state a child's phone
// is in every morning.
func (d *androidDevice) reboot() {
	d.t.Helper()
	d.mustRun(60*time.Second, "reboot")

	// wait-for-device carries no deadline of its own, so the timeout has to come from outside it:
	// a device that never comes back would otherwise block here rather than fail.
	if out, err := d.run(4*time.Minute, "wait-for-device"); err != nil {
		d.t.Fatalf("the device did not come back after the reboot: %v\n%s", err, out)
	}

	deadline := time.Now().Add(4 * time.Minute)
	for {
		out, _ := d.run(20*time.Second, "shell", "getprop", "sys.boot_completed")
		if strings.TrimSpace(out) == "1" {
			break
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("sys.boot_completed never became 1 after the reboot (last read %q)", strings.TrimSpace(out))
		}
		time.Sleep(2 * time.Second)
	}
	// BOOT_COMPLETED is broadcast after the property is already set, so the receiver has not
	// necessarily run yet. Nothing below depends on this sleep — awaitReportedBuild polls — it just
	// keeps the first few polls from being noise.
	time.Sleep(5 * time.Second)

	d.awaitUnlockedUser()
}

// awaitUnlockedUser blocks until user 0 is not merely running but UNLOCKED.
//
// `sys.boot_completed` flips roughly twenty seconds before that happens, and so does the package
// service, so every other readiness signal is satisfied while credential-encrypted storage is still
// shut. Everything the DPC keeps — the enrollment credential, the device token, the recovery secret
// — lives in that storage, so in the gap the app cannot read its own state and the failure names
// SharedPreferences rather than the lock. Measured 2026-09-05 on API 37: this layer failed with
// *"the device did not enrol"* over
// `IllegalStateException: SharedPreferences in credential encrypted storage are not available until
// after user (id 0) is unlocked`, on a device that was fine moments later.
func (d *androidDevice) awaitUnlockedUser() {
	d.t.Helper()

	deadline := time.Now().Add(4 * time.Minute)
	for {
		// `dumpsys user` rather than a property: there is no property for this, and `am unlock-user`
		// answers "could not unlock user" on a device with no credential — it is the platform that
		// unlocks, and the only honest thing to do is wait for it and say so if it never happens.
		out, _ := d.run(20*time.Second, "shell", "dumpsys", "user")
		if strings.Contains(out, "RUNNING_UNLOCKED") {
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("user 0 never reached RUNNING_UNLOCKED after the reboot; every read of the " +
				"encrypted credential store below would have failed naming SharedPreferences " +
				"rather than the lock")
		}
		time.Sleep(2 * time.Second)
	}
}

// enroll runs the device half: the instrumentation that calls ConnectionService.start with the
// extras a provisioning QR would have carried.
//
// The app is force-stopped first. ConnectionService starts its connection loop only when one is not
// already running, so a service left over from an earlier run would take the new extras and ignore
// them — and the test would then be measuring an enrollment that happened against a server which no
// longer exists.
func (d *androidDevice) enroll(serverURL, token string) {
	d.t.Helper()
	d.mustRun(30*time.Second, "shell", "am", "force-stop", dpcPackage)

	out := d.mustRun(3*time.Minute, "shell", "am", "instrument", "-w",
		"-e", "class", enrollClass,
		"-e", argServerURL, serverURL,
		"-e", argEnrollmentToken, token,
		dpcTestRunner)

	// `am instrument` exits 0 whether the tests passed, failed, or never ran — a filter that matches
	// nothing is a green with no test in it. The two lines below are the ones that distinguish them,
	// and both are required: "OK (1 test)" alone would also be printed by a run of some other single
	// test if the filter were ever wrong.
	if strings.Contains(out, "FAILURES!!!") || !strings.Contains(out, "OK (1 test)") {
		d.t.Fatalf("the device did not enrol. `am instrument` said:\n%s", out)
	}
}

// ---- the test ---------------------------------------------------------------

// The whole of FR-15, end to end, on a device.
//
// The sequence is the one a parent would cause: a phone enrolled against a control plane that is
// hosting a NEWER DPC than the phone is running — which is the ordinary state of affairs, since the
// APK on the node is replaced out of band — and a parent who taps "Update app" in the console.
//
// Four independent facts have to line up, and each is read from a different authority:
//
//   - the platform installed a new build      (adb, dumpsys package)
//   - it is the build the server was hosting  (the number read out of the APK the server serves)
//   - the DPC is running again afterwards     (a heartbeat arrives at all, after the install killed
//     the process that would have sent it — this is
//     the MY_PACKAGE_REPLACED receiver, and nothing
//     else in the suite can see it)
//   - the console shows it                    (the heartbeat's version reaches the parent's view)
//
// The last two are the reason the command is acknowledged before the install and not after: the
// process that would have acknowledged it is gone by then, so the version in the heartbeat is the
// only confirmation a parent ever gets.
func TestTheServerReplacesTheDPCOnARealDevice(t *testing.T) {
	d := androidFromEnv(t)
	// Registered before anything is done to the phone, so it covers every failure below including
	// the ones in setup.
	d.dumpDeviceLogOnFailure()

	apkPath := os.Getenv("E2E_ANDROID_NEXT_APK")
	if apkPath == "" {
		t.Fatal("E2E_ANDROID_NEXT_APK is unset; there is no APK for the server to host")
	}
	// The server publishes itself as 10.0.2.2 — the emulator's name for this machine — while this
	// test goes on talking to it on 127.0.0.1. One socket, two names. See emulatorHostAlias.
	h := newHarness(t, withSelfHostedAPK(apkPath), withPublicHost(emulatorHostAlias))
	deviceBase := fmt.Sprintf("http://%s:%d", emulatorHostAlias, h.port)

	// The starting point, read from the platform rather than assumed. A device left on the newer
	// build by a previous run would make everything below pass without an install happening.
	if got := d.installedBuild(); got != d.currentBuild {
		t.Fatalf("the phone is running build %d; this test was set up to update build %d and cannot "+
			"say anything about a device in another state", got, d.currentBuild)
	}

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "Nils' phone")
	_, enrollToken := h.provision(parent.Token, device.ID)

	// The phone enrols itself, over the socket, with the single-use token out of the QR payload.
	// Nothing in this test writes a device row directly: an enrollment forged here would leave the
	// phone holding no credential and unable to receive the command at all.
	d.enroll(deviceBase, enrollToken)

	// The instrumentation took the app's process down with it when it finished — see reboot(). This
	// is what leaves the phone in the state a provisioned one is in: DPC running, started by the
	// boot receiver off the credential the enrollment stored.
	//
	// It is also the last thing here that needs adb. The service that comes up applies the policy,
	// the policy contains `no_debugging_features`, and from that moment the phone is a phone: the
	// only way to see it is the control plane, which is the way a parent sees it too.
	d.reboot()

	// It reports which build it is running, before it is asked to change anything. This is also
	// what proves the service came up and is talking to this server.
	awaitReportedBuild(t, h, parent.Token, device.ID, d.currentBuild, 3*time.Minute)

	cmd := h.issueCommand(parent.Token, device.ID, "UPDATE_APP", nil)
	t.Logf("queued UPDATE_APP %s; the phone should download %d bytes and install build %d",
		cmd.ID, mustFileSize(t, apkPath), d.nextBuild)

	// The platform's own record, if the phone is still reachable at all — see confirmInstalledBuild.
	d.confirmInstalledBuild(d.nextBuild, updateDeadline)

	// The binding assertion: the DPC is alive on the other side of its own replacement, and it is
	// the new build. Without the MY_PACKAGE_REPLACED receiver this is where the test stops — the
	// phone is updated, managed and enforcing nothing, and from the console it looks exactly like a
	// phone that went offline. The number travels heartbeat → upsert → device view, and the DPC
	// reads it from the package manager rather than from a compiled-in constant, so a process that
	// reports build N+1 is a process the platform agrees is build N+1.
	awaitReportedBuild(t, h, parent.Token, device.ID, d.nextBuild, updateDeadline)

	// The command itself was acknowledged before the install — it had to be, since the install kills
	// the process that would answer — so the console must show it done rather than stuck in flight.
	if state := h.commandState(parent.Token, device.ID, cmd.ID); state != "ACKED" {
		t.Errorf("the update command is %q; a parent watching this sees an update that never "+
			"reported back", state)
	}

	// The negative control, on the real device rather than in a unit test: the same command again,
	// with the phone now running exactly what the server hosts. FR-15.3 installs on a strictly greater
	// build, so this must be a no-op — and a no-op that says so, not a failure.
	//
	// It is worth the extra wait because the alternative failure is unpleasant and invisible: an
	// updater that reinstalled an equal build would kill and restart the DPC every time a parent
	// tapped the button, and the phone would look like it was crash-looping.
	again := h.issueCommand(parent.Token, device.ID, "UPDATE_APP", nil)
	final := awaitCommandSettled(t, h, parent.Token, device.ID, again.ID, updateDeadline)
	if final.State != "ACKED" {
		t.Fatalf("the second update command ended as %q: %+v", final.State, final)
	}
	if state, _ := final.Result["state"].(string); !strings.Contains(state, "already running") {
		t.Errorf("the phone answered %q to an update it is already running; it should report that "+
			"it is current, not install anything", state)
	}
	// And it is still the build it was: a decline that quietly reinstalled would show up here as the
	// same number, so this is read after the command settled rather than before it was sent.
	if view := h.deviceView(parent.Token, device.ID); view.State == nil {
		t.Error("the console has no state for this device after the second update command")
	} else if view.State.AppVersionCode != d.nextBuild {
		t.Errorf("the phone reports build %d after an update it should have declined, expected %d",
			view.State.AppVersionCode, d.nextBuild)
	}
}

// awaitReportedBuild waits until the console shows the phone running build want.
//
// The console, not the database and not the device API: this is the number a parent reads, and the
// path it travels — heartbeat, upsert, device view — is the whole reason the two version columns
// exist.
func awaitReportedBuild(t *testing.T, h *harness, parentToken, deviceID string, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last int64
	var lastName string
	for time.Now().Before(deadline) {
		view := h.deviceView(parentToken, deviceID)
		if view.State != nil {
			last, lastName = view.State.AppVersionCode, view.State.AppVersionName
			if last == want {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("the console still shows the phone running %q build %d after %s, expected build %d",
		lastName, last, within, want)
}

// awaitCommandSettled waits for a command to leave the queue, and returns it however it ended.
func awaitCommandSettled(t *testing.T, h *harness, parentToken, deviceID, commandID string, within time.Duration) commandDTO {
	t.Helper()
	deadline := time.Now().Add(within)
	var last commandDTO
	for time.Now().Before(deadline) {
		var list struct {
			Commands []commandDTO `json:"commands"`
		}
		h.call("GET", "/devices/"+deviceID+"/commands", parentToken, nil).
			expect(200).decode(&list)
		for _, c := range list.Commands {
			if c.ID != commandID {
				continue
			}
			last = c
			// Upper case, which is how the store spells them (store.CmdAcked and its siblings) and
			// how the API hands them back. A lower-case comparison here matched nothing: it turned a
			// settled command into a five-minute wait and then a failure naming the state it was
			// looking at.
			if c.State == "ACKED" || c.State == "FAILED" || c.State == "EXPIRED" {
				return c
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("command %s was still %q after %s", commandID, last.State, within)
	return last
}

func mustFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
