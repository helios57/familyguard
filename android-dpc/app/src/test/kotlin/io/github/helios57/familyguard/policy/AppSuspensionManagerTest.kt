package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A platform stand-in for app suspension.
 *
 * @param ignoreSuspend packages the platform accepts and does not act on — the accepted-but-not-in-
 * effect shape the read-back exists to catch. Distinct from [refuse], which is a platform that says
 * no out loud.
 * @param invisible packages [installed] does not report, whatever else it knows about them. This is
 * package-visibility filtering, and it is the reason `installed()` cannot be trusted on its own.
 */
private class FakeAppGateway(
    installed: Set<String>,
    suspended: Set<String> = emptySet(),
    hidden: Set<String> = emptySet(),
    private val refuse: Set<String> = emptySet(),
    private val ignoreSuspend: Set<String> = emptySet(),
    private val ignoreHide: Set<String> = emptySet(),
    private val throwOnSuspend: Set<String> = emptySet(),
    private val throwOnHide: Set<String> = emptySet(),
    private val invisible: Set<String> = emptySet(),
) : AppGateway {
    private val present = installed.toMutableSet()
    private val isSuspended = suspended.toMutableSet()
    private val isHidden = hidden.toMutableSet()

    val suspendCalls = mutableListOf<Pair<List<String>, Boolean>>()

    override fun installed(): Set<String> = (present - invisible).toSet()

    override fun suspended(): Set<String> = isSuspended.toSet()

    override fun hidden(): Set<String> = isHidden.toSet()

    override fun setSuspended(packages: List<String>, suspended: Boolean): Map<String, String> {
        suspendCalls += packages to suspended
        packages.firstOrNull { it in throwOnSuspend }?.let {
            throw SecurityException("$it: the platform threw")
        }
        val refused = LinkedHashMap<String, String>()
        for (pkg in packages) {
            when {
                pkg in refuse -> refused[pkg] = "the platform did not act on it"
                pkg in ignoreSuspend -> Unit // accepted, silently not applied
                suspended -> isSuspended += pkg
                else -> isSuspended -= pkg
            }
        }
        return refused
    }

    override fun setHidden(pkg: String, hidden: Boolean): Boolean {
        if (pkg in throwOnHide) throw SecurityException("$pkg: the platform threw")
        if (pkg in refuse) return false
        if (pkg in ignoreHide) return true // accepted, silently not applied
        if (hidden) isHidden += pkg else isHidden -= pkg
        return true
    }
}

private const val OWN = "io.github.helios57.familyguard"
private const val DIALER = "com.android.dialer"

private val PROTECTED = setOf(OWN, DIALER)

class AppSuspensionPlannerTest {

    @Test
    fun `plans only what is installed and not already in the wanted state`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = listOf("com.game", "com.chat", "com.never.installed"),
            // A package the caller wants hidden is deliberately a different one here; the two lists
            // overlapping has its own rule and its own test below.
            desiredHidden = listOf("com.chat"),
            installed = setOf("com.game", "com.chat", OWN),
            currentlySuspended = setOf("com.chat"),
            currentlyHidden = emptySet(),
            protectedPackages = PROTECTED,
        )

        assertEquals(listOf("com.game"), plan.suspend)
        assertEquals(emptyList<String>(), plan.release)
        assertEquals(listOf("com.chat"), plan.hide)
        assertEquals(listOf("com.never.installed"), plan.absent)
    }

    @Test
    fun `a package no longer wanted is released`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = emptyList(),
            desiredHidden = emptyList(),
            installed = setOf("com.game", OWN),
            currentlySuspended = setOf("com.game"),
            currentlyHidden = setOf("com.game"),
            protectedPackages = PROTECTED,
        )

        assertEquals(listOf("com.game"), plan.release)
        assertEquals(listOf("com.game"), plan.reveal)
        assertEquals(emptyList<String>(), plan.suspend)
    }

    @Test
    fun `a protected package the server named is never suspended`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = listOf(DIALER, OWN, "com.game"),
            desiredHidden = listOf(DIALER),
            installed = setOf(DIALER, OWN, "com.game"),
            currentlySuspended = emptySet(),
            currentlyHidden = emptySet(),
            protectedPackages = PROTECTED,
        )

        assertEquals(listOf("com.game"), plan.suspend)
        assertEquals(emptyList<String>(), plan.hide)
        // Not silently dropped into `absent` either — they are installed; they are simply not ours
        // to touch.
        assertEquals(emptyList<String>(), plan.absent)
    }

    @Test
    fun `a protected package restrained by an older policy is released`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = listOf("com.game"),
            desiredHidden = emptyList(),
            installed = setOf(DIALER, "com.game", OWN),
            currentlySuspended = setOf(DIALER),
            currentlyHidden = setOf(DIALER),
            protectedPackages = PROTECTED,
        )

        // The repair half. Without it a phone restrained by a policy the parent has since deleted
        // stays unable to dial forever, because nothing asks for it any more.
        assertEquals(listOf(DIALER), plan.release)
        assertEquals(listOf(DIALER), plan.reveal)
    }

    @Test
    fun `a suspended package that went invisible is not released`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = listOf("com.game"),
            desiredHidden = emptyList(),
            // `installed` no longer reports it — a visibility read that came back short.
            installed = setOf(OWN),
            currentlySuspended = setOf("com.game"),
            currentlyHidden = emptySet(),
            protectedPackages = PROTECTED,
        )

        // Handing the app back on the strength of a read that failed is the failure this asserts
        // against: the child gets the blocked app, and nothing anywhere is red.
        assertEquals(emptyList<String>(), plan.release)
        assertEquals(listOf("com.game"), plan.absent)
    }

    @Test
    fun `a package no longer wanted is released even after it went invisible`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = emptyList(),
            desiredHidden = emptyList(),
            // Same short read as the test above — but this time nothing wants the package any more.
            installed = setOf(OWN),
            currentlySuspended = setOf("com.game"),
            currentlyHidden = setOf("com.game"),
            protectedPackages = PROTECTED,
        )

        // The mirror image of `a suspended package that went invisible is not released`, and the
        // reason `release` is computed against what is *wanted* rather than against what is
        // installed. Gate the release on `installed` too and the two cases collapse into one rule
        // that is right for the first and permanently wrong for this one: the parent deletes the
        // bedtime, the console shows no rule, and the phone stays suspended forever because a
        // package read stopped reporting an app the platform is still restraining. Every other test
        // in this class releases a package that `installed` also reports, so all of them stay green
        // under that change — this is the only one that does not.
        assertEquals(listOf("com.game"), plan.release)
        assertEquals(listOf("com.game"), plan.reveal)
        assertEquals(emptyList<String>(), plan.suspend)
        // It is not "absent" either: absent means the server asked for a package that is not here,
        // and the server asked for nothing.
        assertEquals(emptyList<String>(), plan.absent)
    }

    @Test
    fun `blank package names are dropped rather than sent to the platform`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = listOf("", "com.game"),
            desiredHidden = listOf(""),
            installed = setOf("com.game", OWN),
            currentlySuspended = emptySet(),
            currentlyHidden = emptySet(),
            protectedPackages = PROTECTED,
        )

        assertEquals(listOf("com.game"), plan.suspend)
        assertEquals(emptyList<String>(), plan.absent)
    }

    @Test
    fun `a package that is hidden is not also suspended`() {
        val plan = AppSuspensionPlanner.plan(
            // What every blocked app looks like: the engine puts it on both lists (FR-5.2).
            desiredSuspended = listOf("com.facebook.katana"),
            desiredHidden = listOf("com.facebook.katana"),
            installed = setOf("com.facebook.katana", OWN),
            currentlySuspended = emptySet(),
            currentlyHidden = emptySet(),
            protectedPackages = PROTECTED,
        )

        // Hiding clears the installed-for-this-user flag, after which the platform answers
        // `isPackageSuspended` for the package with NameNotFoundException — read as "not
        // suspended". Ask for both and the next sync re-requests the suspension, has it refused,
        // and records a failure; and so does the one after that, forever, for every app the family
        // blocks. A hidden app cannot be launched, so there is nothing for suspension to add.
        assertEquals(emptyList<String>(), plan.suspend)
        assertEquals(listOf("com.facebook.katana"), plan.hide)
        assertEquals(emptyList<String>(), plan.absent)
    }

    @Test
    fun `a hidden package the parent unblocked comes back`() {
        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = emptyList(),
            desiredHidden = emptyList(),
            installed = setOf("com.facebook.katana", OWN),
            currentlySuspended = emptySet(),
            currentlyHidden = setOf("com.facebook.katana"),
            protectedPackages = PROTECTED,
        )

        // The reversibility the family blocklist advertises, at the layer that decides it. The
        // other half is [InstalledPackagesTest]: this plan is only reachable if the gateway can
        // still see a package it has hidden.
        assertEquals(listOf("com.facebook.katana"), plan.reveal)
    }
}

class InstalledPackagesTest {

    @Test
    fun `a package the platform hid is still actionable`() {
        val union = InstalledPackages.union(
            present = listOf("com.chat", OWN),
            known = listOf("com.chat", OWN, "com.facebook.katana", "com.long.gone"),
            isHidden = { it == "com.facebook.katana" },
        )

        // Without the hidden package here, nothing downstream can ever reveal it: `hidden()`
        // enumerates this set, so the package is in neither the current-hidden set nor the wanted
        // one, and deleting the rule that hid it does nothing at all.
        assertEquals(setOf("com.chat", OWN, "com.facebook.katana"), union)
        // And the wide read is a candidate list, not an answer: a package the platform merely
        // remembers is not something this DPC can act on, and asking it to would be a refusal
        // reported as a failure on every sync.
        assertFalse("com.long.gone" in union)
    }

    @Test
    fun `the platform is asked only about what the narrow read did not return`() {
        val asked = mutableListOf<String>()

        InstalledPackages.union(
            present = listOf("com.chat", OWN),
            known = listOf("com.chat", OWN, "com.facebook.katana"),
            isHidden = { asked += it; true },
        )

        // One binder call per package, on a phone that reported 504 of them. The answer cannot
        // change the result for a package the narrow read already returned.
        assertEquals(listOf("com.facebook.katana"), asked)
    }
}

class AppSuspensionManagerTest {

    private fun manager(gateway: AppGateway) =
        AppSuspensionManager(gateway, PROTECTED, ownPackage = OWN)

    @Test
    fun `suspends and hides what the policy asks for`() {
        val gateway = FakeAppGateway(installed = setOf("com.game", "com.chat", OWN, DIALER))

        val outcome = manager(gateway).apply(listOf("com.game", "com.chat"), listOf("com.game"))

        assertTrue(outcome.toString(), outcome.ok)
        // `com.game` is on both lists, so it is hidden and not also suspended — hiding is the
        // stronger of the two and the platform stops reporting suspension once it lands.
        assertEquals(listOf("com.chat"), outcome.suspended)
        assertEquals(listOf("com.game"), outcome.hidden)
        assertEquals(setOf("com.chat"), gateway.suspended())
        assertEquals(setOf("com.game"), gateway.hidden())
    }

    @Test
    fun `releases before it suspends`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", DIALER, OWN),
            suspended = setOf(DIALER),
        )

        manager(gateway).apply(listOf("com.game"), emptyList())

        // Ordering is the whole assertion: a process killed between the two calls must leave the
        // phone less restrained, not more. The dialer is freed first.
        assertEquals(listOf(DIALER) to false, gateway.suspendCalls.first())
        assertEquals(listOf("com.game") to true, gateway.suspendCalls[1])
    }

    @Test
    fun `a package that is not installed is reported, and is not a failure`() {
        val gateway = FakeAppGateway(installed = setOf(OWN, DIALER))

        val outcome = manager(gateway).apply(listOf("com.not.here"), emptyList())

        assertEquals(listOf("com.not.here"), outcome.absent)
        assertTrue(outcome.toString(), outcome.ok)
        assertTrue(outcome.missing.isEmpty())
    }

    @Test
    fun `a refused package is a failure naming that package`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", "com.chat", OWN),
            refuse = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(listOf("com.game", "com.chat"), emptyList())

        assertFalse(outcome.ok)
        assertEquals("suspend: the platform did not act on it", outcome.failures["com.game"])
        // The rest of the batch still landed: one refusal must not cost the others.
        assertEquals(setOf("com.chat"), gateway.suspended())
    }

    @Test
    fun `accepted and not in effect is reported as loudly as a refusal`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            ignoreSuspend = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(listOf("com.game"), emptyList())

        assertFalse(outcome.ok)
        assertTrue(outcome.failures.isEmpty())
        assertEquals("suspension requested, accepted, and not in effect", outcome.missing["com.game"])
    }

    @Test
    fun `accepted hiding that did not take effect is reported`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            ignoreHide = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(emptyList(), listOf("com.game"))

        assertFalse(outcome.ok)
        assertEquals("hiding requested, accepted, and not in effect", outcome.missing["com.game"])
    }

    @Test
    fun `a package on both lists reports the hiding, because that is what was asked for`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            // Inert: nothing asks this platform to suspend the package at all.
            ignoreSuspend = setOf("com.game"),
            ignoreHide = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(listOf("com.game"), listOf("com.game"))

        assertFalse(outcome.ok)
        // One line per package, and it names the call that was actually made. Reporting a missing
        // *suspension* here would charge a failure to a call this code deliberately never makes,
        // on every sync, for every app the family blocks.
        assertEquals(1, outcome.missing.size)
        assertEquals("hiding requested, accepted, and not in effect", outcome.missing["com.game"])
        assertTrue(gateway.suspendCalls.isEmpty())
    }

    @Test
    fun `unblocking a hidden app brings it back`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.facebook.katana", OWN),
            hidden = setOf("com.facebook.katana"),
        )

        val outcome = manager(gateway).apply(emptyList(), emptyList())

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf("com.facebook.katana"), outcome.revealed)
        assertEquals(emptySet<String>(), gateway.hidden())
    }

    @Test
    fun `a batch that throws charges every package in it`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", "com.chat", OWN),
            throwOnSuspend = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(listOf("com.game", "com.chat"), emptyList())

        assertFalse(outcome.ok)
        // The exception names one package; the call took two, and neither landed. Charging only the
        // named one would report the other as suspended when it is not.
        assertEquals(setOf("com.chat", "com.game"), outcome.failures.keys)
    }

    @Test
    fun `a hide the platform declined is a failure naming that package`() {
        val gateway = FakeAppGateway(installed = setOf("com.game", OWN), refuse = setOf("com.game"))

        val outcome = manager(gateway).apply(emptyList(), listOf("com.game"))

        assertFalse(outcome.ok)
        assertEquals("hide: the platform declined", outcome.failures["com.game"])
    }

    @Test
    fun `a hide that throws is a failure naming that package`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            throwOnHide = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(emptyList(), listOf("com.game"))

        assertFalse(outcome.ok)
        assertTrue(outcome.failures.getValue("com.game").startsWith("hide: "))
    }

    @Test
    fun `an installed-package read that cannot see this app is not trusted`() {
        // Package-visibility filtering, or a platform read that failed: everything comes back short.
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            invisible = setOf("com.game", OWN),
        )

        val outcome = manager(gateway).apply(listOf("com.game"), emptyList())

        // Without this control the outcome reads: nothing installed, nothing to do, all clean —
        // while the child keeps the blocked app. The DPC can always see itself, so its absence is
        // proof the read is not measuring the device.
        assertFalse(outcome.ok)
        assertTrue(outcome.failures.getValue("!installed").contains("cannot be trusted"))
    }

    @Test
    fun `a critical package restrained by something else on the device is reported`() {
        val gateway = FakeAppGateway(
            installed = setOf(DIALER, OWN),
            suspended = setOf(DIALER),
            // The platform refuses to release it, so it is still restrained after the apply.
            refuse = setOf(DIALER),
        )

        val outcome = manager(gateway).apply(emptyList(), emptyList())

        assertFalse(outcome.ok)
        assertEquals(listOf(DIALER), outcome.stillRestrained)
    }

    @Test
    fun `nothing to do makes no platform call at all`() {
        val gateway = FakeAppGateway(
            installed = setOf("com.game", OWN),
            suspended = setOf("com.game"),
        )

        val outcome = manager(gateway).apply(listOf("com.game"), emptyList())

        assertTrue(outcome.toString(), outcome.ok)
        assertTrue(gateway.suspendCalls.isEmpty())
    }
}
