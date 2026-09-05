package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.ManagedApp
import io.github.helios57.familyguard.policy.FakeGateway
import io.github.helios57.familyguard.policy.HardeningManager
import io.github.helios57.familyguard.policy.compliantClock
import io.github.helios57.familyguard.update.ApkIdentity
import io.github.helios57.familyguard.update.UpdateOutcome
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

private const val INSTALL = EnforcementEngine.RESTRICTION_INSTALL_APPS
private const val UNINSTALL = EnforcementEngine.RESTRICTION_UNINSTALL_APPS

private fun declared(pkg: String, code: Long) = ManagedApp(
    packageName = pkg,
    versionCode = code,
    versionName = "1.$code",
    checksum = "Y2hlY2tzdW0",
    size = 1024,
    url = "https://guard.example.com/api/v1/device/apps/$pkg/$code.apk",
)

/**
 * The applier with the phone under the test's control.
 *
 * [restrictionsWhenStaging] and [restrictionsWhenCommitting] are the point of most of this class:
 * they record what was in effect at the moment each half ran, which is the only way to assert the
 * window is narrow. Asserting on the gateway's *final* state cannot tell a lift that closed
 * immediately from one that stayed open for the whole download.
 */
private class Phone(
    initialRestrictions: Set<String> = setOf(INSTALL, UNINSTALL),
) {
    val gateway = FakeGateway(initial = initialRestrictions)
    val hardening = HardeningManager(gateway, compliantClock())

    /** package -> version code the phone currently has. */
    val installed = mutableMapOf<String, Long>()

    /** What this app is recorded as the installer of. */
    val ours = mutableSetOf<String>()

    var stageRefusals = mutableMapOf<String, String>()
    var commitFailures = mutableMapOf<String, String>()
    var uninstallFailures = mutableMapOf<String, String>()

    val staged = mutableListOf<String>()
    val committed = mutableListOf<String>()
    val uninstalled = mutableListOf<String>()
    var restrictionsWhenStaging: Set<String>? = null
    var restrictionsWhenCommitting: Set<String>? = null
    var restrictionsWhenUninstalling: Set<String>? = null

    fun applier() = ManagedAppApplier(
        hardening = hardening,
        installedVersion = { installed[it] },
        installedByThisApp = { ours.toSet() },
        stage = { app ->
            staged += app.packageName
            restrictionsWhenStaging = gateway.current()
            val refusal = stageRefusals[app.packageName]
            if (refusal != null) {
                UpdateOutcome.Refused(refusal)
            } else {
                UpdateOutcome.Staged(
                    identity = ApkIdentity(app.packageName, app.versionCode, app.versionName, "aa".repeat(32)),
                    fromVersionCode = installed[app.packageName] ?: 0L,
                ) {
                    restrictionsWhenCommitting = gateway.current()
                    commitFailures[app.packageName]?.let { throw IllegalStateException(it) }
                    committed += app.packageName
                    installed[app.packageName] = app.versionCode
                    ours += app.packageName
                }
            }
        },
        uninstall = { pkg ->
            restrictionsWhenUninstalling = gateway.current()
            val failure = uninstallFailures[pkg]
            if (failure == null) {
                uninstalled += pkg
                installed -= pkg
                ours -= pkg
            }
            failure
        },
    )
}

class ManagedAppApplierTest {

    @Test
    fun `it installs a declared app the phone does not have`() {
        val phone = Phone()
        val outcome = phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 7))))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf("ch.example.muplay"), phone.committed)
        assertEquals(7L, phone.installed["ch.example.muplay"])
        assertEquals("declared=1 installed=1 removed=0", outcome.summary)
    }

    /**
     * The convergence property, and the one that decides whether this feature is usable at all.
     * Without it every sync re-downloads every declared application — tens of megabytes, every
     * fifteen minutes, on a child's mobile data.
     */
    @Test
    fun `a phone that already has the declared build downloads nothing`() {
        val phone = Phone()
        phone.installed["ch.example.muplay"] = 7
        phone.ours += "ch.example.muplay"

        val outcome = phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 7))))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("nothing may be staged when the phone is already converged", emptyList<String>(), phone.staged)
        assertEquals("declared=1 converged", outcome.summary)
    }

    @Test
    fun `a newer declared build is installed over the old one`() {
        val phone = Phone()
        phone.installed["ch.example.muplay"] = 7
        phone.ours += "ch.example.muplay"

        val outcome = phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 8))))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf("ch.example.muplay"), phone.committed)
        assertEquals(8L, phone.installed["ch.example.muplay"])
    }

    @Test
    fun `withdrawing an app removes it from the phone`() {
        val phone = Phone()
        phone.installed["ch.example.muplay"] = 7
        phone.ours += "ch.example.muplay"

        val outcome = phone.applier().apply(DesiredState(managedApps = emptyList()))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf("ch.example.muplay"), phone.uninstalled)
        assertEquals("declared=0 installed=0 removed=1", outcome.summary)
    }

    /**
     * The bound on removal, and the reason this applier cannot wipe a phone: the declared set says
     * what should be present, and says nothing about the thousand other packages a phone carries.
     *
     * **Calibrated, and it did not go red.** Breaking the removal set two ways — dropping the
     * "still declared" filter, and dropping the candidate list — reddens five other tests in this
     * class and never this one. That is information rather than a pass: the applier is only ever
     * handed the packages it installed, so within this layer there is no way for it to reach one
     * it did not. The bug this guards against is real but lives one level down, in
     * `AndroidInstaller.installedByThisApp()`, where "installed by this app" is separated from
     * "installed" — and that is platform code, measurable only on a device. The control that binds
     * to it is `ManagedInstallTest.theRemovalCandidatesAreOnlyWhatThisAppInstalled`.
     *
     * Kept anyway, as the statement of the contract the wiring has to satisfy.
     */
    @Test
    fun `it never removes a package it did not install`() {
        val phone = Phone()
        phone.installed["com.android.dialer"] = 1
        phone.installed["com.whatever.sideloaded"] = 1
        // `ours` stays empty: neither of those came from a declared set.

        val outcome = phone.applier().apply(DesiredState(managedApps = emptyList()))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(
            "an applier that removed what it did not install would wipe the phone on its first pass",
            emptyList<String>(), phone.uninstalled,
        )
        assertEquals("declared=0 converged", outcome.summary)
    }

    /**
     * The narrow window, measured where it matters rather than asserted about the end state.
     *
     * A download is minutes on a phone's connection. If the restriction were lifted around it, the
     * child could install anything they liked for the whole of that time, on every sync that
     * changes an application. What the lift has to cover is the installer session, and nothing
     * before it.
     */
    @Test
    fun `the install restriction is in effect while downloading and lifted only to commit`() {
        val phone = Phone()
        phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 7))))

        assertTrue(
            "the download ran with $INSTALL lifted: ${phone.restrictionsWhenStaging}",
            INSTALL in (phone.restrictionsWhenStaging ?: emptySet()),
        )
        assertFalse(
            "the session was opened with $INSTALL still in effect, which is what the platform " +
                "refuses: ${phone.restrictionsWhenCommitting}",
            INSTALL in (phone.restrictionsWhenCommitting ?: setOf(INSTALL)),
        )
        assertEquals(
            "the restrictions must be exactly as they were found once the pass is over",
            setOf(INSTALL, UNINSTALL), phone.gateway.current(),
        )
    }

    @Test
    fun `the uninstall restriction is lifted for the removal and put back`() {
        val phone = Phone()
        phone.installed["ch.example.muplay"] = 7
        phone.ours += "ch.example.muplay"

        phone.applier().apply(DesiredState(managedApps = emptyList()))

        assertFalse(
            "the removal ran with $UNINSTALL in effect: ${phone.restrictionsWhenUninstalling}",
            UNINSTALL in (phone.restrictionsWhenUninstalling ?: setOf(UNINSTALL)),
        )
        assertEquals(setOf(INSTALL, UNINSTALL), phone.gateway.current())
    }

    /**
     * What is restored is what was read back, not what was asked for. A parent who has left child
     * installs ON must not find them off because a managed app was installed.
     */
    @Test
    fun `a restriction that was not in effect is not switched on by the lift`() {
        val phone = Phone(initialRestrictions = setOf(UNINSTALL))
        phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 7))))

        assertEquals(
            "the lift hardened the phone past what the parent chose",
            setOf(UNINSTALL), phone.gateway.current(),
        )
    }

    @Test
    fun `the restrictions go back even when the install throws`() {
        val phone = Phone()
        phone.commitFailures["ch.example.muplay"] = "INSTALL_FAILED_INSUFFICIENT_STORAGE"

        val outcome = phone.applier().apply(DesiredState(managedApps = listOf(declared("ch.example.muplay", 7))))

        assertFalse("a failed install must be reported, not swallowed", outcome.ok)
        assertEquals(
            "the parent needs the platform's own words",
            "INSTALL_FAILED_INSUFFICIENT_STORAGE", outcome.problems["ch.example.muplay"],
        )
        assertEquals(
            "an install that threw left the phone with installs allowed",
            setOf(INSTALL, UNINSTALL), phone.gateway.current(),
        )
    }

    /**
     * One bad entry must not keep the rest of a child's applications off the phone. This is the
     * same reason `CompositeApplier` runs every applier: a partial failure that stops the pass
     * turns one problem into an unmanaged phone.
     */
    @Test
    fun `one refused app does not stop the others`() {
        val phone = Phone()
        phone.stageRefusals["ch.example.broken"] = "the downloaded file is not the one the server published a checksum for"

        val outcome = phone.applier().apply(
            DesiredState(managedApps = listOf(declared("ch.example.broken", 1), declared("ch.example.muplay", 7)))
        )

        assertFalse(outcome.ok)
        assertEquals(listOf("ch.example.muplay"), phone.committed)
        assertTrue(outcome.problems["ch.example.broken"]!!.contains("checksum"))
        assertEquals("declared=2 installed=1 removed=0", outcome.summary)
    }

    @Test
    fun `a removal the platform refuses is reported against the package`() {
        val phone = Phone()
        phone.installed["ch.example.muplay"] = 7
        phone.ours += "ch.example.muplay"
        phone.uninstallFailures["ch.example.muplay"] = "status=-1 DELETE_FAILED_INTERNAL_ERROR"

        val outcome = phone.applier().apply(DesiredState(managedApps = emptyList()))

        assertFalse("a removal that did not happen must not report clean", outcome.ok)
        assertTrue(outcome.problems["ch.example.muplay"]!!.contains("DELETE_FAILED"))
    }

    /**
     * The empty case, which is what almost every sync of almost every phone is. It must cost
     * nothing: no lift, no platform call, no download.
     */
    @Test
    fun `a phone with no declared apps touches nothing`() {
        val phone = Phone()
        val outcome = phone.applier().apply(DesiredState())

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("declared=0 converged", outcome.summary)
        assertEquals(
            "a converged pass must not open the restriction window at all",
            emptyList<String>(), phone.gateway.calls,
        )
    }
}
