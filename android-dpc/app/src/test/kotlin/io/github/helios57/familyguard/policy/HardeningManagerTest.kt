package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.enforce.EnforcementEngine
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A platform stand-in that can behave the way real ones do: accept a call and apply it, accept a
 * call and quietly do nothing, or throw. `internal` so `ClockPolicyManagerTest` can use the same
 * one; a second copy would be a second thing to keep in step with [RestrictionGateway].
 */
internal class FakeGateway(
    initial: Set<String> = emptySet(),
    private val ignore: Set<String> = emptySet(),
    private val throwOn: Set<String> = emptySet(),
) : RestrictionGateway {
    private val state = initial.toMutableSet()
    val calls = mutableListOf<String>()

    override fun current(): Set<String> = state.toSet()

    override fun add(key: String) {
        calls += "add:$key"
        if (key in throwOn) throw SecurityException("$key is not supported on this build")
        if (key !in ignore) state += key
    }

    override fun clear(key: String) {
        calls += "clear:$key"
        if (key in throwOn) throw SecurityException("$key cannot be cleared on this build")
        if (key !in ignore) state -= key
    }
}

class HardeningManagerTest {

    private val safeBoot = EnforcementEngine.RESTRICTION_SAFE_BOOT
    private val debugging = EnforcementEngine.RESTRICTION_DEBUGGING
    private val installApps = EnforcementEngine.RESTRICTION_INSTALL_APPS
    private val factoryReset = EnforcementEngine.RESTRICTION_FACTORY_RESET

    @Test
    fun `it applies the plan and reports what it did`() {
        val gateway = FakeGateway(initial = setOf(safeBoot, installApps))
        val outcome = HardeningManager(gateway, compliantClock()).apply(listOf(safeBoot, debugging))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf(debugging), outcome.added)
        assertEquals(listOf(installApps), outcome.cleared)
        assertEquals(setOf(safeBoot, debugging), gateway.current())
    }

    /**
     * The read-back earning its place. This gateway accepts every call and applies none of them,
     * which is how an unsupported restriction behaves on some OEM builds — and an applier that
     * trusted the calls it made would report a hardened device forever.
     */
    @Test
    fun `a platform that accepts a call and does nothing is caught`() {
        val gateway = FakeGateway(ignore = setOf(debugging))
        val outcome = HardeningManager(gateway, compliantClock()).apply(listOf(safeBoot, debugging))

        assertFalse("the applier believed its own bookkeeping", outcome.ok)
        assertEquals(listOf(debugging), outcome.missing)
        assertTrue("no exception was thrown, so failures must be empty", outcome.failures.isEmpty())
        // The positive control: the other restriction really did land, so `missing` is a finding
        // about one call and not about a gateway that ignores everything.
        assertTrue(safeBoot in gateway.current())
    }

    @Test
    fun `one rejected restriction does not cost the device the others`() {
        val gateway = FakeGateway(throwOn = setOf(debugging))
        val outcome = HardeningManager(gateway, compliantClock()).apply(listOf(safeBoot, debugging, installApps))

        assertFalse(outcome.ok)
        assertEquals(setOf(debugging), outcome.failures.keys)
        assertTrue(outcome.failures.getValue(debugging).startsWith("add: "))
        assertEquals(
            "the restrictions that were not rejected must still be in effect",
            setOf(safeBoot, installApps),
            gateway.current(),
        )
    }

    /**
     * Clearing restores the escape hatch, so it goes first: a process killed halfway through
     * provisioning must leave the phone less locked down than intended, never more.
     */
    @Test
    fun `clears run before adds`() {
        val gateway = FakeGateway(initial = setOf(installApps, factoryReset))
        HardeningManager(gateway, compliantClock()).apply(listOf(safeBoot))

        val firstAdd = gateway.calls.indexOfFirst { it.startsWith("add:") }
        val lastClear = gateway.calls.indexOfLast { it.startsWith("clear:") }
        assertTrue("there was no add to order against: ${gateway.calls}", firstAdd >= 0)
        assertTrue("there was no clear to order against: ${gateway.calls}", lastClear >= 0)
        assertTrue("calls were ${gateway.calls}", lastClear < firstAdd)
    }

    /**
     * FR-2.3 / NFR-6. A phone that cannot be wiped from recovery can only be rescued through the
     * control plane, which is the one component that might be why it needs rescuing. If clearing
     * fails, that has to reach the caller as a finding — not as a log line inside a green result.
     */
    @Test
    fun `a phone left un-wipeable is never reported as ok`() {
        val stubborn = FakeGateway(initial = setOf(factoryReset), ignore = setOf(factoryReset))
        val outcome = HardeningManager(stubborn, compliantClock()).apply(listOf(safeBoot))
        assertFalse(outcome.ok)
        assertEquals(listOf(factoryReset), outcome.stillForbidden)

        // And the negative control: the same restriction on a gateway that honours the clear leaves
        // an ok outcome, so `stillForbidden` is not simply always populated.
        val willing = FakeGateway(initial = setOf(factoryReset))
        val fixed = HardeningManager(willing, compliantClock()).apply(listOf(safeBoot))
        assertTrue(fixed.toString(), fixed.ok)
        assertTrue(fixed.stillForbidden.isEmpty())
        assertFalse(factoryReset in willing.current())
    }

    /**
     * The pre-sync baseline is applied from the engine's constant, so a change to what "hardened"
     * means reaches provisioning and boot without anyone remembering to update a second list.
     */
    @Test
    fun `the baseline applied at provisioning is the engine's own`() {
        val gateway = FakeGateway()
        val outcome = HardeningManager(gateway, compliantClock()).applyBaseline()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(
            EnforcementEngine.BASELINE_RESTRICTIONS.sorted(),
            gateway.current().sorted(),
        )
        assertFalse(
            "the unprompted baseline must leave the phone wipeable",
            factoryReset in gateway.current(),
        )
    }

    /**
     * The boot path is a floor, not a target.
     *
     * `applyBaseline` runs on every boot, including on a phone that has been syncing for months. If
     * it went through the same authoritative path as a server-sent state, that phone would come back
     * from a reboot with every restriction the baseline does not name — `no_debugging_features`
     * among them — *cleared*, and would stay that way until the next successful sync. A child could
     * open that window on demand by restarting the phone, and nothing about the device would look
     * wrong during it.
     */
    @Test
    fun `a boot never weakens a device that has already synced`() {
        val synced = FakeGateway(initial = EnforcementEngine.BASELINE_RESTRICTIONS.toSet() + debugging + installApps)
        val outcome = HardeningManager(synced, compliantClock()).applyBaseline()

        assertTrue(outcome.toString(), outcome.ok)
        assertTrue("the boot path cleared something: ${outcome.cleared}", outcome.cleared.isEmpty())
        assertTrue(
            "a reboot dropped no_debugging_features, which the last sync had set",
            debugging in synced.current(),
        )
        assertTrue(installApps in synced.current())

        // The negative control: the authoritative path over the same device DOES clear them, so the
        // assertion above is a property of applyBaseline and not of a gateway that ignores clears.
        val sameDevice = FakeGateway(initial = EnforcementEngine.BASELINE_RESTRICTIONS.toSet() + debugging + installApps)
        HardeningManager(sameDevice, compliantClock()).apply(EnforcementEngine.BASELINE_RESTRICTIONS)
        assertFalse(debugging in sameDevice.current())
        assertFalse(installApps in sameDevice.current())
    }

    /**
     * The one thing a boot still takes away. FR-2.3 does not care how the device got into a state
     * where factory reset is blocked; it says the next boot must leave it wipeable again.
     */
    @Test
    fun `a boot still clears a forbidden restriction it finds in effect`() {
        val gateway = FakeGateway(initial = setOf(factoryReset, debugging))
        val outcome = HardeningManager(gateway, compliantClock()).applyBaseline()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf(factoryReset), outcome.cleared)
        assertFalse(factoryReset in gateway.current())
        assertTrue("it cleared more than the forbidden one", debugging in gateway.current())
    }

    // ---- a device a recovery code has released (FR-12.6) ------------------------------------

    /**
     * The defect this method was written for. `BootReceiver` applies the baseline unconditionally,
     * so a released phone that reboots came back with six of the eight restrictions back on —
     * silently, and precisely when the sync that would legitimately end the release is the thing
     * that cannot happen, because a release is only ever used when the control plane is out of
     * reach.
     */
    @Test
    fun `a released device that reboots stays released`() {
        val gateway = FakeGateway()

        val outcome = HardeningManager(gateway, compliantClock()).applyReleasedFloor()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(emptyList<String>(), outcome.added)
        assertTrue("it hardened a released device", gateway.current().isEmpty())
        assertTrue("it called the platform at all", gateway.calls.isEmpty())
    }

    /**
     * The calibration for the test above, and the reason it is evidence rather than a tautology: on
     * the identical starting state the ordinary boot path sets the whole baseline. Delete the
     * `released` branch in `AdminReceiver.applyBaseline` and the two outcomes become the same one.
     */
    @Test
    fun `the ordinary boot path on the same device hardens all six`() {
        val gateway = FakeGateway()

        val outcome = HardeningManager(gateway, compliantClock()).applyBaseline()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(EnforcementEngine.BASELINE_RESTRICTIONS.sorted(), outcome.added.sorted())
        assertEquals(EnforcementEngine.BASELINE_RESTRICTIONS.toSet(), gateway.current())
    }

    @Test
    fun `a released device is still left wipeable`() {
        // FR-2.3 does not have an exception for a released phone, and this is the half of the floor
        // that gives an escape hatch back rather than taking one away.
        val gateway = FakeGateway(initial = setOf(factoryReset, safeBoot))

        val outcome = HardeningManager(gateway, compliantClock()).applyReleasedFloor()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(listOf(factoryReset), outcome.cleared)
        assertFalse(factoryReset in gateway.current())
    }

    @Test
    fun `it leaves alone a restriction it did not set`() {
        // A floor, not a plan: `applyReleasedFloor` must not become a second authoritative path that
        // clears whatever it finds. The device may have restrictions from an OEM or from a previous
        // policy generation, and unsetting those is a change nobody asked for.
        val gateway = FakeGateway(initial = setOf(safeBoot, installApps))

        HardeningManager(gateway, compliantClock()).applyReleasedFloor()

        assertEquals(setOf(safeBoot, installApps), gateway.current())
    }

    @Test
    fun `it does not touch the clock`() {
        // Automatic network time is enforcement (FR-2.2). A released phone enforces nothing, and
        // `HardeningOutcome.clock` is null rather than a success so that a later baseline's genuine
        // clock failure cannot be overwritten by this path's clean outcome.
        val clockGateway = FakeClockGateway(enabled = false)

        val outcome = HardeningManager(FakeGateway(), ClockPolicyManager(clockGateway))
            .applyReleasedFloor()

        assertNull(outcome.clock)
        assertEquals(emptyList<String>(), clockGateway.calls)

        // The positive control: the same fake does get called on the path that is meant to.
        HardeningManager(FakeGateway(), ClockPolicyManager(clockGateway)).applyBaseline()
        assertTrue(clockGateway.calls.isNotEmpty())
    }
}
