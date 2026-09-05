package io.github.helios57.familyguard

import android.app.PendingIntent
import android.app.admin.DevicePolicyManager
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.os.Build
import android.os.UserManager
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import io.github.helios57.familyguard.update.AndroidInstaller
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * FR-16 where it can actually be measured: a device owner installing a package that is not itself,
 * on a phone carrying its own restrictions.
 *
 * The question this class exists to answer is not "does PackageInstaller work" — it is whether the
 * DPC's *own* hardening stops it. `no_install_unknown_sources` is in
 * [EnforcementEngine.BASELINE_RESTRICTIONS], so it is on before the phone has ever reached the
 * server; `no_install_apps` joins it whenever a parent turns child installs off; and
 * `no_uninstall_apps` is in the baseline too, which decides whether a package can ever be taken
 * back off once the declared set drops it. If any of the three binds the device owner as well as
 * the child, then a declared app set is unimplementable in the shape it was designed in, and the
 * feature has to lift the restriction around each install instead.
 *
 * **The answer, measured on API 37 rather than reasoned about:** two of the three bind, and they
 * fail in two different shapes.
 *
 *  - `no_install_unknown_sources` does NOT bind a device-owner session. It is the one that is in
 *    effect during every managed install this product will ever do, and it is therefore the one
 *    that had to be measured before anything else — so it is not lifted.
 *  - `no_install_apps` DOES bind it. `PackageInstaller.createSession` throws
 *    `SecurityException: User restriction prevents installing`, **synchronously, before a session
 *    exists** — not the `INSTALL_FAILED_USER_RESTRICTED` status this file originally expected.
 *  - `no_uninstall_apps` DOES bind it, and *does* fail as a status: `STATUS_FAILURE_BLOCKED` (2),
 *    `DELETE_FAILED_USER_RESTRICTED`, with the package still installed. A caller guarding only
 *    against exceptions would read that as a successful withdrawal.
 *
 * Reasoning about any of it from the platform source is exactly the mistake this repo keeps
 * finding: the exemption for a device owner lives in `PackageInstallerService`, it has moved
 * between releases, and one of the two refusals is delivered to a receiver nobody reads. So it is
 * measured, on API 37 and on the API 29 floor.
 *
 * Every test here puts the restrictions back and removes the fixture package, whatever happened —
 * see [restoreTheDevice]. Left behind, `no_install_apps` on a device whose control plane is
 * unreachable is a phone nobody can put software on.
 */
@RunWith(AndroidJUnit4::class)
class ManagedInstallTest {

    private lateinit var context: Context
    private lateinit var users: UserManager
    private lateinit var installer: PackageInstaller
    private lateinit var gateway: DpmRestrictionGateway

    /** What the fixture module builds; staged into this APK's assets by :app's build script. */
    private val fixturePackage = "io.github.helios57.familyguard.fixture"

    private fun inEffect(): Set<String> {
        val bundle = users.userRestrictions
        return bundle.keySet().filterTo(mutableSetOf()) { bundle.getBoolean(it) }
    }

    private fun isInstalled(pkg: String): Boolean = runCatching {
        context.packageManager.getPackageInfo(pkg, 0)
    }.isSuccess

    @Before
    fun requireDeviceOwnerAndAFixture() {
        context = InstrumentationRegistry.getInstrumentation().targetContext
        users = context.getSystemService(UserManager::class.java)
        installer = context.packageManager.packageInstaller
        val dpm = context.getSystemService(DevicePolicyManager::class.java)
        assertTrue(
            "this test measures a managed device and this one is not managed by ${context.packageName}. " +
                "Provision it first:  adb shell dpm set-device-owner " +
                "${context.packageName}/.admin.AdminReceiver",
            dpm.isDeviceOwnerApp(context.packageName),
        )
        gateway = DpmRestrictionGateway(
            dpm,
            ComponentName(context.packageName, DpmRestrictionGateway.ADMIN_RECEIVER),
            users,
        )
        // A leftover from a previous run would make an install test pass without installing — and
        // removing it is itself blocked by the baseline this device carries, so the cleanup has to
        // clear the restriction first. Measured: a run aborted mid-class (the emulator's
        // surfaceflinger takes system_server down under load) leaves the fixture installed AND
        // `no_uninstall_apps` in effect, and the naive cleanup below it then failed silently under
        // `runCatching` — the next run reported "the fixture is not installed yet and is already a
        // removal candidate", which names a consequence three steps away from the cause.
        removeAnyLeftoverFixture()
    }

    @After
    fun restoreTheDevice() {
        runCatching { gateway.clear(EnforcementEngine.RESTRICTION_INSTALL_APPS) }
        runCatching { gateway.clear(EnforcementEngine.RESTRICTION_UNINSTALL_APPS) }
        runCatching { removeAnyLeftoverFixture() }
        runCatching { DpmRestrictionGateway.hardeningManager(context)?.applyBaseline() }
        assertTrue(
            "this test left ${EnforcementEngine.RESTRICTION_INSTALL_APPS} in effect on the device",
            EnforcementEngine.RESTRICTION_INSTALL_APPS !in inEffect(),
        )
    }

    /**
     * Takes the fixture off whatever state the device is in, restrictions included.
     *
     * Both the setup and the teardown need it, and both need it to actually work rather than to be
     * attempted: a cleanup that can be blocked is a cleanup that leaves the next test measuring the
     * previous one. The restrictions are cleared unconditionally — clearing one that is not set is
     * a no-op, and reading first would only add a way to be wrong.
     */
    private fun removeAnyLeftoverFixture() {
        if (!isInstalled(fixturePackage)) return
        runCatching { gateway.clear(EnforcementEngine.RESTRICTION_UNINSTALL_APPS) }
        val status = uninstallFixture()
        assertEquals(
            "a leftover $fixturePackage could not be removed ($status), so this class would " +
                "measure the state a previous run left rather than the one it set up",
            PackageInstaller.STATUS_SUCCESS,
            status.code,
        )
    }

    /**
     * The narrow lift, measured on the platform rather than reasoned about.
     *
     * [ManagedAppApplier][io.github.helios57.familyguard.sync.ManagedAppApplier] downloads outside
     * the window and opens it around nothing but the installer session. That is only correct if the
     * platform checks the restriction when the operation *opens* — if it re-checked at commit, or
     * asynchronously while the install proceeds, the narrow window would produce
     * `INSTALL_FAILED_USER_RESTRICTED` on a phone whose parent had simply turned child installs off,
     * and the applier would retry it forever.
     *
     * So the restriction is put BACK before the commit and the install still has to succeed. A
     * failure here is not a bug in this test: it means the lift has to stay open until the platform
     * says the install finished, and the applier's window has to widen to match.
     */
    @Test
    fun theInstallRestrictionIsCheckedWhenTheSessionOpensAndNotAtCommit() {
        val install = EnforcementEngine.RESTRICTION_INSTALL_APPS
        gateway.add(install)
        assertTrue(
            "$install is not in effect, so restoring it below would restore nothing and this test " +
                "would prove only that an unrestricted install works. In effect: ${inEffect().sorted()}",
            install in inEffect(),
        )

        val staged = stageFixture(FIXTURE_V1)
        val status = awaitStatus(ACTION_INSTALL) { sender ->
            gateway.clear(install)
            val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
            params.setAppPackageName(fixturePackage)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                params.setInstallReason(PackageManager.INSTALL_REASON_POLICY)
            }
            val id = installer.createSession(params)
            installer.openSession(id).use { session ->
                session.openWrite("fixture", 0, staged.length()).use { sink ->
                    staged.inputStream().use { it.copyTo(sink) }
                    session.fsync(sink)
                }
                // Back on BEFORE the commit, which is the whole question.
                gateway.add(install)
                session.commit(sender)
            }
        }

        assertEquals(
            "the platform refused a session that was OPENED with the restriction lifted and " +
                "committed with it back on: $status. ManagedAppApplier's window is too narrow and " +
                "has to stay open until the install reports.",
            PackageInstaller.STATUS_SUCCESS,
            status.code,
        )
        assertTrue("the platform reports $fixturePackage absent after a successful session", isInstalled(fixturePackage))
        assertTrue("the restriction was not back on by the time the install finished", install in inEffect())
    }

    /**
     * The whole declared-set lifecycle on a real phone: install, upgrade, withdraw (FR-16.3,
     * FR-16.5).
     *
     * The two fixture builds differ only in their version code, which is what makes the middle step
     * an upgrade rather than a second install — and the version read back from the package manager
     * is what proves the upgrade happened. `isInstalled` alone cannot: it is true before and after.
     *
     * The withdrawal runs inside [HardeningManager.withoutRestrictions], because that is what the
     * applier does and the point of this test is the product's sequence rather than a raw platform
     * call. It is also not optional: `no_uninstall_apps` is in the baseline, so on a phone whose
     * hardening has been applied — every real one — an unwrapped removal comes back
     * `STATUS_FAILURE_BLOCKED`. This test found that out by going red on exactly that status while
     * the three restriction tests above were all green, which is the ordering-dependent shape a
     * test is most likely to hide: run first, before anything applies the baseline, it passes.
     */
    @Test
    fun aDeclaredAppIsInstalledThenUpgradedThenWithdrawn() {
        assertEquals(
            "could not install the first build",
            PackageInstaller.STATUS_SUCCESS, installFixture(FIXTURE_V1).code,
        )
        assertEquals("the phone is not running the first build", 1L, installedVersion(fixturePackage))

        assertEquals(
            "could not install the second build over the first",
            PackageInstaller.STATUS_SUCCESS, installFixture(FIXTURE_V2).code,
        )
        assertEquals(
            "the second session reported success and the phone still runs the first build",
            2L, installedVersion(fixturePackage),
        )

        // The baseline is what a real phone carries, so put it there rather than hoping the test
        // ran before something else applied it. Without this the assertion below is about whichever
        // order JUnit chose today.
        gateway.add(EnforcementEngine.RESTRICTION_UNINSTALL_APPS)
        val hardening = requireNotNull(DpmRestrictionGateway.hardeningManager(context)) {
            "this device is managed by ${context.packageName} and hardeningManager() still " +
                "returned null; the lift below cannot be exercised"
        }
        val removal = hardening.withoutRestrictions(
            listOf(EnforcementEngine.RESTRICTION_UNINSTALL_APPS),
        ) { uninstallFixture() }

        assertEquals(
            "could not remove the package when it left the declared set, with the same lift the " +
                "applier uses: $removal",
            PackageInstaller.STATUS_SUCCESS, removal.code,
        )
        assertTrue("the platform still reports $fixturePackage present", !isInstalled(fixturePackage))
        assertTrue(
            "the lift did not put ${EnforcementEngine.RESTRICTION_UNINSTALL_APPS} back after the " +
                "removal, so a child could uninstall anything until the next sync",
            EnforcementEngine.RESTRICTION_UNINSTALL_APPS in inEffect(),
        )
    }

    /**
     * The control the JVM layer cannot carry: `installedByThisApp` really does separate what this
     * app installed from what is merely installed.
     *
     * `ManagedAppApplierTest` asserts the applier only removes from that set, and calibrating it
     * showed it binds to nothing — the applier is handed the set and cannot reach outside it. The
     * separation itself is `getInstallSourceInfo`, which exists only on a device. If it were to
     * return this app for everything, the first pass of a declared set would uninstall the phone.
     */
    @Test
    fun theRemovalCandidatesAreOnlyWhatThisAppInstalled() {
        val installerApi = AndroidInstaller(context)

        val before = installerApi.installedByThisApp()
        assertTrue(
            "the fixture is not installed yet and is already a removal candidate: $before",
            fixturePackage !in before,
        )
        // The positive control for the set being readable at all. A method that returned an empty
        // set on every call would satisfy the assertion above and every other one in this class.
        assertTrue(
            "this device reports ${installedPackageCount()} packages and none of them is this app's " +
                "own, so installedByThisApp() is reading something that is not the package list",
            installedPackageCount() > 1,
        )

        assertEquals(PackageInstaller.STATUS_SUCCESS, installFixture(FIXTURE_V1).code)
        val after = installerApi.installedByThisApp()

        assertTrue(
            "a package this app installed is not a removal candidate, so a withdrawn app could " +
                "never be taken off: $after",
            fixturePackage in after,
        )
        assertTrue(
            "installedByThisApp() named this app itself; a managed-app pass would find the device " +
                "owner undeclared and uninstall it",
            context.packageName !in after,
        )
        assertTrue(
            "installedByThisApp() returned ${after.size} of ${installedPackageCount()} packages, " +
                "which is not a filter — the first pass of a declared set would wipe this phone",
            after.size < installedPackageCount(),
        )
    }

    /**
     * Why `no_install_unknown_sources` is NOT in [ManagedAppApplier.LIFTED], and why that matters
     * more than the two that are.
     *
     * It is in [EnforcementEngine.BASELINE_RESTRICTIONS], so it is on before the phone has ever
     * reached the server and it is on during every managed install this product will ever do. If it
     * bound the device owner, FR-16 would be unimplementable without lifting the one restriction
     * that keeps a child from sideloading — for the whole length of an install, on every sync.
     *
     * Measured 2026-09-05 on API 37: it does not. A device-owner session with
     * `INSTALL_REASON_POLICY` installs while it is in effect. The restriction governs the
     * confirmation UI an ordinary installer has to go through, and a device owner does not go
     * through it.
     */
    @Test
    fun noInstallUnknownSourcesDoesNotBindTheDeviceOwner() {
        val unknown = EnforcementEngine.RESTRICTION_UNKNOWN_SOURCES
        gateway.add(unknown)
        // The positive control, and it is not ceremony: addUserRestriction accepts an unknown key
        // silently, so an install succeeding is otherwise consistent with the restriction having
        // never been applied at all — which is the likelier of the two explanations.
        assertTrue(
            "$unknown is not in effect after this device owner set it, so the install below would " +
                "prove nothing. In effect: ${inEffect().sorted()}",
            unknown in inEffect(),
        )

        val status = installFixture()
        assertEquals(
            "a device owner could not install with only $unknown in effect: $status. That " +
                "restriction is in the baseline, so FR-16 would have to lift it around every " +
                "install — and lifting it is exactly what opens the child's own sideloading path.",
            PackageInstaller.STATUS_SUCCESS,
            status.code,
        )
        assertTrue("the platform reports $fixturePackage absent after a successful session", isInstalled(fixturePackage))
        assertTrue("the restriction did not survive the install it did not block", unknown in inEffect())
    }

    /**
     * Why the lift exists at all, and what its absence looks like.
     *
     * `no_install_apps` binds the device owner as well as the child. Measured 2026-09-05 on API 37:
     * `PackageInstaller.createSession` throws `SecurityException: User restriction prevents
     * installing` — **synchronously, before a session exists**, not as a status delivered to the
     * result receiver. That shape is the reason [ManagedAppApplier] has to survive a `commit` that
     * throws rather than only one that reports, and the reason its restore is in a `finally`.
     *
     * This test asserts the refusal. A day when it stops throwing is a day the platform changed
     * under this feature, and the right response is to look rather than to celebrate a green — the
     * lift would then be unnecessary, and an unnecessary lift is a window that need not be open.
     */
    @Test
    fun noInstallAppsBindsTheDeviceOwnerToo() {
        val install = EnforcementEngine.RESTRICTION_INSTALL_APPS
        gateway.add(install)
        assertTrue(
            "$install is not in effect after this device owner set it; the refusal below would " +
                "then be some other refusal. In effect: ${inEffect().sorted()}",
            install in inEffect(),
        )

        val thrown = runCatching { installFixture() }.exceptionOrNull()
        assertTrue(
            "installing under $install did not throw. It returned " +
                "${runCatching { installFixture().toString() }.getOrElse { "another throw: $it" }}. " +
                "If the platform no longer binds the device owner here, ManagedAppApplier's lift is " +
                "unnecessary and should be removed rather than left open.",
            thrown is SecurityException,
        )
        assertTrue("$fixturePackage was installed by a session that threw", !isInstalled(fixturePackage))
    }

    /**
     * The other half, and it fails in a different shape — which is the point of measuring it
     * separately rather than assuming the symmetry.
     *
     * `no_uninstall_apps` is in the baseline too, so a declared set that can add but never remove
     * is a console that lies. Measured 2026-09-05 on API 37: the removal is **not** a throw. The
     * session is created, the call returns, and the failure arrives at the result receiver as
     * `STATUS_FAILURE_BLOCKED` (2) with `DELETE_FAILED_USER_RESTRICTED`, with the package still
     * present. A caller that only guarded against exceptions would read that as a successful
     * withdrawal and the console would show an app as removed that is still on the phone.
     */
    @Test
    fun noUninstallAppsBindsTheDeviceOwnerAndFailsAsAStatusNotAThrow() {
        val uninstall = EnforcementEngine.RESTRICTION_UNINSTALL_APPS
        assertEquals(
            "could not install the fixture to then fail to remove it",
            PackageInstaller.STATUS_SUCCESS, installFixture().code,
        )

        gateway.add(uninstall)
        assertTrue(
            "$uninstall is not in effect after this device owner set it; the removal below would " +
                "prove nothing. In effect: ${inEffect().sorted()}",
            uninstall in inEffect(),
        )

        val status = uninstallFixture()
        assertEquals(
            "removing under $uninstall reported $status rather than being blocked. If the platform " +
                "no longer binds the device owner here, ManagedAppApplier's uninstall lift is " +
                "unnecessary and should be removed rather than left open.",
            PackageInstaller.STATUS_FAILURE_BLOCKED,
            status.code,
        )
        assertTrue(
            "the removal was reported as blocked and the package is gone anyway, so the status " +
                "and the phone disagree",
            isInstalled(fixturePackage),
        )
    }

    // ---- the plumbing --------------------------------------------------------------------------

    private data class Status(val code: Int, val message: String) {
        override fun toString() = "status=$code ${message.ifEmpty { "(no message)" }}"
    }

    /**
     * Runs one installer session to completion and reports what the platform said.
     *
     * A session's outcome arrives on a broadcast, and the receiver has to be registered before the
     * commit — a status delivered with nothing listening is the same shape as a session that never
     * reported, and both read as a timeout.
     */
    private fun awaitStatus(action: String, body: (android.content.IntentSender) -> Unit): Status {
        val results = ArrayBlockingQueue<Status>(4)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context, intent: Intent) {
                val code = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, Int.MIN_VALUE)
                // PENDING_USER_ACTION is not an outcome; it is the platform asking for a tap that a
                // device owner should never be asked for. Recorded as itself so the failure names it.
                results.offer(Status(code, intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE).orEmpty()))
            }
        }
        val filter = IntentFilter(action)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            context.registerReceiver(receiver, filter)
        }
        try {
            val intent = Intent(action).setPackage(context.packageName)
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
            body(PendingIntent.getBroadcast(context, action.hashCode(), intent, flags).intentSender)
            return results.poll(TIMEOUT_SECONDS, TimeUnit.SECONDS)
                ?: Status(Int.MIN_VALUE, "no status arrived within ${TIMEOUT_SECONDS}s")
        } finally {
            runCatching { context.unregisterReceiver(receiver) }
        }
    }

    /** Copies one fixture build out of the test APK's assets and returns the file. */
    private fun stageFixture(asset: String): File {
        val staged = File(context.cacheDir, asset)
        InstrumentationRegistry.getInstrumentation().context.assets.open(asset).use { source ->
            staged.outputStream().use { source.copyTo(it) }
        }
        return staged
    }

    /** What the package manager says is installed, or null when the package is absent. */
    private fun installedVersion(pkg: String): Long? = runCatching {
        @Suppress("DEPRECATION")
        context.packageManager.getPackageInfo(pkg, 0).longVersionCode
    }.getOrNull()

    /** The positive control for every "installedByThisApp did not name X" assertion. */
    private fun installedPackageCount(): Int {
        @Suppress("DEPRECATION")
        return context.packageManager.getInstalledPackages(0).size
    }

    private fun installFixture(asset: String = FIXTURE_V1): Status {
        val staged = stageFixture(asset)
        return awaitStatus(ACTION_INSTALL) { sender ->
            val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
            params.setAppPackageName(fixturePackage)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                params.setInstallReason(PackageManager.INSTALL_REASON_POLICY)
            }
            val id = installer.createSession(params)
            installer.openSession(id).use { session ->
                session.openWrite("fixture", 0, staged.length()).use { sink ->
                    staged.inputStream().use { it.copyTo(sink) }
                    session.fsync(sink)
                }
                session.commit(sender)
            }
        }
    }

    private fun uninstallFixture(): Status =
        awaitStatus(ACTION_UNINSTALL) { sender -> installer.uninstall(fixturePackage, sender) }

    private companion object {
        /**
         * The two builds the fixture module produces. They differ only in `versionCode` (1 and 2),
         * which is what makes the second install an upgrade — and what makes the upgrade
         * *observable*: `isInstalled` is true before and after, so only the version code can tell
         * an upgrade that happened from one that silently did not.
         */
        const val FIXTURE_V1 = "fixture-app-v1.apk"
        const val FIXTURE_V2 = "fixture-app-v2.apk"
        const val ACTION_INSTALL = "io.github.helios57.familyguard.test.INSTALL_STATUS"
        const val ACTION_UNINSTALL = "io.github.helios57.familyguard.test.UNINSTALL_STATUS"
        const val TIMEOUT_SECONDS = 60L
    }
}
