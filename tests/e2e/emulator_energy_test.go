package e2e

// FR-26.4 and FR-26.5 on a real Android device, read from the authorities rather than from the app:
// Android's own VPN record says which uids the tunnel carries, and the server's energy endpoint says
// what the phone reported. Driven by tests/android/energy.sh; run on its own it SKIPS.
//
// The filter needs a list over https, and a bench has no certificate a release-rule phone trusts, so
// this uses the public list the pilot uses. A bench without internet cannot bring the tunnel up;
// that is reported as NOT MEASURED, never as a pass.

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const publicFilterList = "https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt"

func TestAdFreeAppsBypassTheFilterAndThePhoneReportsItsEnergy(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, _ := managedOnEmulator(t, h, d)
	filterUp(t, h, parent.Token, child.ID, device.ID)

	// ---- the bypass, as Android records it ----
	var m []string
	uid := func(pkg string) int {
		out, _ := d.run(30*time.Second, "shell", "pm", "list", "packages", "-U", pkg)
		m := regexp.MustCompile(`package:` + regexp.QuoteMeta(pkg) + ` uid:(\d+)`).FindStringSubmatch(out)
		if m == nil {
			return -1
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	vpn, _ := d.run(30*time.Second, "shell", "dumpsys", "vpn_management")
	m = regexp.MustCompile(`Uids: <\{([^}]*)\}> OwnerUid: ` + strconv.Itoa(uid("io.github.helios57.familyguard"))).FindStringSubmatch(vpn)
	if m == nil {
		t.Fatalf("no VPN owned by FamilyGuard in dumpsys vpn_management:\n%s", vpn)
	}
	carried := func(u int) bool {
		for _, r := range strings.Split(m[1], ",") {
			var lo, hi int
			if _, err := fmt.Sscanf(strings.TrimSpace(r), "%d-%d", &lo, &hi); err == nil && u >= lo && u <= hi {
				return true
			}
		}
		return false
	}
	dialer, _ := d.run(30*time.Second, "shell", "telecom", "get-default-dialer")
	for _, pkg := range []string{"com.google.android.gms", strings.TrimSpace(dialer), "io.github.helios57.familyguard"} {
		u := uid(pkg)
		if u < 0 {
			t.Errorf("%s is not on this image, so its bypass is not measured", pkg)
			continue
		}
		if carried(u) {
			t.Errorf("%s (uid %d) is carried by the tunnel; it should go around it. Uids: %s", pkg, u, m[1])
		}
	}
	// The control: an app that may show ads is still filtered, so the ranges are not simply empty.
	if u := uid("com.android.chrome"); u < 0 || !carried(u) {
		t.Errorf("Chrome (uid %d) is not carried by the tunnel — the bypass took more than it should. Uids: %s", u, m[1])
	}

	// ---- the filter's notification after a sync ----
	// Every sync tells the filter service the policy again, and a tunnel that already matches is kept.
	// Until 0.6.28 that path re-posted "Starting the ad filter…" and never took it back, so a running
	// filter announced it was starting for good (measured on a family phone, 2026-09-28).
	syncCmd := h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
	if c := awaitCommandSettled(t, h, parent.Token, device.ID, syncCmd.ID, 3*time.Minute); c.State != "ACKED" {
		t.Fatalf("SYNC_POLICY ended %s", c.State)
	}
	time.Sleep(3 * time.Second)
	shade, _ := d.run(30*time.Second, "shell", "dumpsys", "notification", "--noredact")
	if !strings.Contains(shade, "Ad filter on") {
		t.Errorf("after a sync the filter's notification does not say it is on")
	}
	if strings.Contains(shade, "Starting the ad filter") {
		t.Errorf("after a sync the running filter's notification says it is starting")
	}

	// ---- the energy report, as the server holds it ----
	// The policy change above woke the phone at least once more, so there are two reports of one run.
	var e energyDTO
	deadline := time.Now().Add(2 * time.Minute)
	for {
		h.call(http.MethodGet, "/devices/"+device.ID+"/energy", parent.Token, nil).expect(http.StatusOK).decode(&e)
		if e.Samples >= 2 && e.Total.Minutes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 2 min the server holds %d energy samples and %.2f measured minutes; the phone does not report", e.Samples, e.Total.Minutes)
		}
		time.Sleep(5 * time.Second)
	}
	if wakes := e.Total.StreamOpens + e.Total.Events + e.Total.OtherSyncs; wakes < 1 {
		t.Errorf("the phone synced after the policy change and reported %d wake-ups: %+v", wakes, e.Total)
	}
	if e.Total.CPUMs <= 0 {
		t.Errorf("the phone reported no CPU time over %.2f minutes of syncing: %+v", e.Total.Minutes, e.Total)
	}
}

// filterUp switches the ad filter on with the public list and waits for the phone to say its tunnel is
// up. The phone says so in its next heartbeat, and nothing makes one soon after the list has been
// fetched and compiled — so it is asked to sync while this waits.
func filterUp(t *testing.T, h *harness, parentToken, childID, deviceID string) {
	t.Helper()
	h.patchPolicy(parentToken, childID, map[string]any{"ad_filter": true, "ad_filter_list_url": publicFilterList})
	deadline := time.Now().Add(4 * time.Minute)
	nudged := time.Time{}
	for {
		if time.Since(nudged) > 30*time.Second {
			h.issueCommand(parentToken, deviceID, "SYNC_POLICY", nil)
			nudged = time.Now()
		}
		st := h.deviceView(parentToken, deviceID).State
		if st != nil && st.AdFilterRunning != nil && *st.AdFilterRunning {
			return
		}
		if time.Now().After(deadline) {
			reason := ""
			if st != nil {
				reason = st.AdFilterReason
			}
			t.Skipf("NOT MEASURED: the tunnel did not come up within 4 min (reason %q); the list is fetched from the internet", reason)
		}
		time.Sleep(5 * time.Second)
	}
}

// tunnelRoutes is the route list of FamilyGuard's tunnel as Android's connectivity service holds it,
// and whether a tunnel was found at all.
func (d *androidDevice) tunnelRoutes() (string, bool) {
	out, _ := d.run(30*time.Second, "shell", "dumpsys", "connectivity")
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "InterfaceName: tun") {
			continue
		}
		if m := regexp.MustCompile(`Routes: \[([^\]]*)\]`).FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// FR-26.4 on a real device, read from Android's own record of the tunnel's routes: with the screen on
// the tunnel carries everything (a default route), five minutes after the screen goes off it carries
// only DNS (the resolver's /32 and no default route), and the screen coming on brings the default
// route back at once. The phone's energy report then holds time in both routes. Driven by
// tests/android/route.sh; run on its own it SKIPS.
func TestTheFilterCarriesOnlyDNSFiveMinutesAfterTheScreenGoesOff(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, _ := managedOnEmulator(t, h, d)
	defer d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
	filterUp(t, h, parent.Token, child.ID, device.ID)

	full := func() (bool, string) {
		r, ok := d.tunnelRoutes()
		if !ok {
			t.Fatalf("no FamilyGuard tunnel in dumpsys connectivity")
		}
		return strings.Contains(r, "0.0.0.0/0"), r
	}
	awaitRoute := func(wantFull bool, within time.Duration, what string) time.Duration {
		t.Helper()
		start := time.Now()
		for {
			if f, r := full(); f == wantFull {
				return time.Since(start)
			} else if time.Since(start) > within {
				t.Fatalf("after %v: %s never happened; routes [%s]", within, what, r)
			}
			time.Sleep(3 * time.Second)
		}
	}
	awaitRoute(true, 60*time.Second, "the full route with the screen on")

	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_SLEEP")
	off := time.Now()
	time.Sleep(4 * time.Minute)
	if f, r := full(); !f {
		t.Errorf("4 min after the screen went off the route is already narrowed; it waits 5. Routes [%s]", r)
	}
	awaitRoute(false, 3*time.Minute, "the route narrowing to DNS after the screen went off")
	took := time.Since(off)
	t.Logf("the route narrowed to DNS %v after the screen went off", took.Round(time.Second))
	if took < 5*time.Minute {
		t.Errorf("the route narrowed after %v; it must wait 5 minutes", took)
	}
	if _, r := full(); !strings.Contains(r, "100.88.0.1/32") {
		t.Errorf("the narrowed tunnel does not route its resolver, so DNS is no longer filtered: [%s]", r)
	}
	// A minute narrowed, so the report has something to count.
	time.Sleep(time.Minute)
	if f, r := full(); f {
		t.Errorf("the route widened again with the screen still off: [%s]", r)
	}

	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
	t.Logf("the full route came back %v after the screen came on",
		awaitRoute(true, 30*time.Second, "the full route after the screen came on").Round(time.Second))

	// The screen coming on opened the stream, which is a sync and a heartbeat with the report.
	var e energyDTO
	deadline := time.Now().Add(2 * time.Minute)
	for {
		h.call(http.MethodGet, "/devices/"+device.ID+"/energy", parent.Token, nil).expect(http.StatusOK).decode(&e)
		if e.Total.RouteDNSMs != nil && *e.Total.RouteDNSMs >= 45_000 && e.Total.RouteFullMs != nil && *e.Total.RouteFullMs > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the phone ran the full route and a minute of DNS only, and its report says full=%s dns=%s",
				msText(e.Total.RouteFullMs), msText(e.Total.RouteDNSMs))
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("the report: full route %d s, DNS only %d s", *e.Total.RouteFullMs/1000, *e.Total.RouteDNSMs/1000)
}

func msText(v *int64) string {
	if v == nil {
		return "not reported"
	}
	return strconv.FormatInt(*v, 10) + " ms"
}
