package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.enforce.EnforcementEngine
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RestrictionPlannerTest {

    private val safeBoot = EnforcementEngine.RESTRICTION_SAFE_BOOT
    private val debugging = EnforcementEngine.RESTRICTION_DEBUGGING
    private val installApps = EnforcementEngine.RESTRICTION_INSTALL_APPS
    private val factoryReset = EnforcementEngine.RESTRICTION_FACTORY_RESET

    @Test
    fun `it adds what is missing and clears what is no longer wanted`() {
        val plan = RestrictionPlanner.plan(
            current = setOf(safeBoot, installApps),
            desired = listOf(safeBoot, debugging),
        )
        assertEquals(listOf(debugging), plan.add)
        assertEquals(listOf(installApps), plan.clear)
    }

    @Test
    fun `a state already in effect produces no calls at all`() {
        val plan = RestrictionPlanner.plan(
            current = setOf(safeBoot, debugging),
            desired = listOf(debugging, safeBoot),
        )
        assertTrue("add=${plan.add} clear=${plan.clear}", plan.isEmpty)
    }

    /**
     * FR-2.3 / NFR-6, enforced on the phone rather than trusted from the wire.
     *
     * The server filters `no_factory_reset` out before it sends anything, so this can only be
     * reached by a control plane that is not the product's own — which is exactly the case worth
     * defending against, because the phone is the half that cannot be rolled back.
     */
    @Test
    fun `a forbidden restriction is never applied, however it arrives`() {
        val plan = RestrictionPlanner.plan(
            current = setOf(safeBoot),
            desired = listOf(safeBoot, debugging) + EnforcementEngine.FORBIDDEN_RESTRICTIONS,
        )
        // The positive control: this plan has to be doing something, or the absence below is free.
        assertEquals(listOf(debugging), plan.add)
        for (r in EnforcementEngine.FORBIDDEN_RESTRICTIONS) {
            assertTrue("the planner would have set $r", r !in plan.add)
        }
        assertTrue("nothing needed clearing here", plan.clear.isEmpty())
    }

    @Test
    fun `a forbidden restriction already in effect is cleared, whoever set it`() {
        val plan = RestrictionPlanner.plan(
            current = setOf(safeBoot, factoryReset),
            desired = listOf(safeBoot),
        )
        assertEquals(
            "the device must be returned to a wipeable state",
            listOf(factoryReset),
            plan.clear,
        )
        assertTrue(plan.add.isEmpty())
    }

    /**
     * The device is ours to manage, but a restriction this app has never heard of belongs to someone
     * else — an OEM, or a control dropped from MANAGED last release. Clearing it would be a change
     * nobody asked for and nobody would see.
     */
    @Test
    fun `an unmanaged restriction is left alone`() {
        val plan = RestrictionPlanner.plan(
            current = setOf(safeBoot, "no_oem_special_thing"),
            desired = listOf(safeBoot),
        )
        assertTrue("add=${plan.add} clear=${plan.clear}", plan.isEmpty)
    }

    @Test
    fun `an empty desired state clears everything this app manages and nothing else`() {
        val plan = RestrictionPlanner.plan(
            current = RestrictionPlanner.MANAGED + "no_oem_special_thing",
            desired = emptyList(),
        )
        assertEquals(RestrictionPlanner.MANAGED.sorted(), plan.clear)
        assertTrue(plan.add.isEmpty())
    }

    /**
     * The planner's forbidden filter and the engine's are the same set, and the engine's is the one
     * the server mirrors. Two lists that can disagree is the shape of every drift bug in this
     * project, so there is one list.
     */
    @Test
    fun `every forbidden restriction is managed, so it can always be cleared`() {
        assertTrue(EnforcementEngine.FORBIDDEN_RESTRICTIONS.isNotEmpty())
        assertTrue(
            "a forbidden restriction outside MANAGED could never be cleared once set",
            RestrictionPlanner.MANAGED.containsAll(EnforcementEngine.FORBIDDEN_RESTRICTIONS),
        )
    }

    // ------------------------------------------------------------------ floor ----
    //
    // `floor` is what runs before the server has been reached — at provisioning compliance and on
    // every boot. The whole reason it is not `plan` is that a boot must never weaken the device, and
    // that difference is invisible on a fresh phone: on an empty `current` the two agree exactly.
    // These tests are therefore all about a device that is *already* carrying something.

    @Test
    fun `the floor adds what is missing`() {
        val plan = RestrictionPlanner.floor(
            current = setOf(safeBoot),
            required = listOf(safeBoot, debugging),
        )
        assertEquals(listOf(debugging), plan.add)
        assertTrue("a floor has nothing to clear here: ${plan.clear}", plan.clear.isEmpty())
    }

    /**
     * The defect this method exists to prevent. A phone that has been syncing for months reboots;
     * the boot path applies the pre-sync floor; under [RestrictionPlanner.plan] every restriction
     * the floor does not name — `no_install_apps` and `no_debugging_features` among them — would be
     * *cleared* until the next successful sync. A child can open that window on demand by restarting
     * the phone, and nothing about the device would look wrong during it.
     */
    @Test
    fun `the floor never takes away what a sync put there`() {
        val current = setOf(safeBoot, debugging, installApps)
        val floor = RestrictionPlanner.floor(current, listOf(safeBoot))
        assertTrue("the floor cleared ${floor.clear}", floor.clear.isEmpty())
        assertTrue(floor.isEmpty)

        // The negative control, over the same inputs: `plan` DOES clear them. Without it the
        // assertion above would also hold for a planner that had stopped computing `clear` at all.
        val authoritative = RestrictionPlanner.plan(current, listOf(safeBoot))
        assertEquals(listOf(debugging, installApps), authoritative.clear)
    }

    /**
     * The one thing a floor still takes away. FR-2.3 does not care how the device got into a state
     * where factory reset is blocked — an earlier release, a different DPC, a bad response — only
     * that the next boot leaves it wipeable again.
     */
    @Test
    fun `the floor still clears a forbidden restriction, and only that`() {
        val plan = RestrictionPlanner.floor(
            current = setOf(factoryReset, debugging, "no_oem_special_thing"),
            required = listOf(safeBoot),
        )
        assertEquals(listOf(factoryReset), plan.clear)
        assertEquals("the floor must still reach its own required set", listOf(safeBoot), plan.add)
    }

    /**
     * The same defence as `plan`'s, on the path that does not go through the server at all. A
     * forbidden restriction cannot arrive here from a response — [floor] is called with a compiled-in
     * constant — so this is a guard against the constant itself being edited, which is precisely the
     * mistake the 5.5 calibration already caught once in `BASELINE_RESTRICTIONS`.
     */
    @Test
    fun `a forbidden restriction in the required set is never applied`() {
        val plan = RestrictionPlanner.floor(
            current = emptySet(),
            required = listOf(safeBoot) + EnforcementEngine.FORBIDDEN_RESTRICTIONS,
        )
        // The positive control: this plan has to be doing something, or the absence below is free.
        assertEquals(listOf(safeBoot), plan.add)
        for (r in EnforcementEngine.FORBIDDEN_RESTRICTIONS) {
            assertTrue("the floor would have set $r", r !in plan.add)
        }
    }

    /**
     * On a device carrying nothing, the two are indistinguishable — which is why every test above
     * seeds `current`. Stated here so the next reader does not conclude from a passing fresh-device
     * test that the distinction is covered.
     */
    @Test
    fun `on a fresh device the floor and the authoritative plan agree`() {
        val required = EnforcementEngine.BASELINE_RESTRICTIONS
        assertEquals(
            RestrictionPlanner.plan(emptySet(), required),
            RestrictionPlanner.floor(emptySet(), required),
        )
        assertEquals(
            required.sorted(),
            RestrictionPlanner.floor(emptySet(), required).add,
        )
    }
}
