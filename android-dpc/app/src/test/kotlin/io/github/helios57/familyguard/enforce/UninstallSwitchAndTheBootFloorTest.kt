package io.github.helios57.familyguard.enforce

import io.github.helios57.familyguard.policy.RestrictionPlanner
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-5.7, and specifically the half the shared vectors structurally cannot reach.
 *
 * `vectors.json` replays [EnforcementEngine.compute] and nothing else, so it can say that a parent
 * who allows uninstalling gets a desired state without `no_uninstall_apps` in it. It cannot say
 * anything about the **boot floor**, which is a different code path with a different input: it runs
 * before any policy is known and therefore honours no switch at all.
 *
 * That difference is a deliberate design decision rather than an oversight, and an undocumented
 * decision is one the next edit silently reverses. So it is pinned here, in both directions:
 * the floor still applies the restriction, and the next authoritative sync still takes it away
 * again. The second half is what makes the first acceptable — without it, one reboot would close a
 * hatch a parent opened and nothing would ever reopen it.
 */
class UninstallSwitchAndTheBootFloorTest {

    private val uninstall = EnforcementEngine.RESTRICTION_UNINSTALL_APPS

    private fun stateWith(allowUninstall: Boolean) = EnforcementEngine.compute(
        Input(
            settings = Settings(
                allowChildInstalls = true,
                allowUninstall = allowUninstall,
                timezone = "Europe/Zurich",
                version = 3,
            ),
            installed = listOf(App(pkg = "com.example.game")),
            now = "2026-08-17T14:00:00+02:00",
        )
    )

    /**
     * The positive control for everything below. An absence proves nothing unless the same engine,
     * on the same input shape, demonstrably produces the presence.
     */
    @Test
    fun `the switch decides whether a synced device is told to restrict uninstalling`() {
        assertTrue(
            "with the switch off the restriction must be in the desired state",
            uninstall in stateWith(allowUninstall = false).userRestrictions,
        )
        assertFalse(
            "with the switch on the desired state must not carry it",
            uninstall in stateWith(allowUninstall = true).userRestrictions,
        )
    }

    /**
     * The floor runs before the server has ever been reached, so there is no parent decision for it
     * to honour — a phone that has never synced has nobody to ask. It keeps the restriction.
     */
    @Test
    fun `the pre-sync boot floor applies it whatever the switch says`() {
        assertTrue(
            "no_uninstall_apps must stay in the baseline the boot path applies",
            uninstall in EnforcementEngine.BASELINE_RESTRICTIONS,
        )
        val plan = RestrictionPlanner.floor(
            current = emptySet(),
            required = EnforcementEngine.BASELINE_RESTRICTIONS,
        )
        assertTrue("a boot must add it on a phone that has none of it", uninstall in plan.add)
    }

    /**
     * And the sync after that boot takes it off again, which is what bounds the window to one sync
     * interval instead of forever. This is the assertion that would go red if the restriction were
     * ever moved somewhere [RestrictionPlanner.MANAGED] does not cover — where it would be applied
     * by the floor and then never cleared by anything.
     */
    @Test
    fun `the next authoritative sync clears what the boot floor put back`() {
        val afterReboot = EnforcementEngine.BASELINE_RESTRICTIONS.toSet()
        assertTrue("the fixture must start from the restricted state", uninstall in afterReboot)

        val plan = RestrictionPlanner.plan(
            current = afterReboot,
            desired = stateWith(allowUninstall = true).userRestrictions,
        )
        assertTrue(
            "a sync carrying the parent's switch must clear it, or the reboot would be permanent",
            uninstall in plan.clear,
        )
    }
}
