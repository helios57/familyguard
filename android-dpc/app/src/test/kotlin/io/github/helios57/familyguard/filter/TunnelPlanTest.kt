package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The four reasons the tunnel does not come up, and the one case where it does.
 *
 * Each refusal is a phone that keeps working. They are asserted individually *and* in order,
 * because the order is the message a parent reads: a filter that is switched off should say so,
 * not report that there is no resolver.
 */
class TunnelPlanTest {

    private val on = FilterPolicy(enabled = true, mode = RouteMode.FULL)
    private val resolvers = listOf("192.0.2.53")

    @Test
    fun `an enabled filter with a list and a resolver runs`() {
        val decision = TunnelPlan.decide(on, upstream = resolvers, ruleCount = 181_117)

        assertEquals(TunnelDecision.Run(RouteMode.FULL, resolvers), decision)
    }

    @Test
    fun `the mode the parent chose is the mode that runs`() {
        val decision = TunnelPlan.decide(
            on.copy(mode = RouteMode.DNS_ONLY),
            upstream = resolvers,
            ruleCount = 10,
        )

        assertEquals(RouteMode.DNS_ONLY, (decision as TunnelDecision.Run).mode)
    }

    @Test
    fun `a filter that is switched off does not run`() {
        val decision = TunnelPlan.decide(
            FilterPolicy(enabled = false),
            upstream = resolvers,
            ruleCount = 181_117,
        )

        assertStands(decision, "switched off")
    }

    @Test
    fun `a stood-down tunnel does not come back on its own`() {
        val decision = TunnelPlan.decide(on, resolvers, ruleCount = 181_117, stoodDown = true)

        assertStands(decision, "stood down")
    }

    @Test
    fun `an empty index is not worth a tunnel`() {
        val decision = TunnelPlan.decide(on, upstream = resolvers, ruleCount = 0)

        // Not a detail: a tunnel with nothing to block carries every packet on the phone through
        // this code for no benefit whatsoever, which is pure added risk.
        assertStands(decision, "no filter list")
    }

    @Test
    fun `a network with no resolver is not one to take DNS away from`() {
        val decision = TunnelPlan.decide(on, upstream = emptyList(), ruleCount = 181_117)

        assertStands(decision, "no resolver")
    }

    @Test
    fun `being switched off is reported ahead of every other reason`() {
        // All four refusals at once. The parent switched it off; that is what they need to be told,
        // and "no filter list has been compiled yet" would send them looking for a fault.
        val decision = TunnelPlan.decide(
            FilterPolicy(enabled = false),
            upstream = emptyList(),
            ruleCount = 0,
            stoodDown = true,
        )

        assertStands(decision, "switched off")
    }

    @Test
    fun `standing down is reported ahead of the states it causes`() {
        val decision = TunnelPlan.decide(on, upstream = emptyList(), ruleCount = 0, stoodDown = true)

        assertStands(decision, "stood down")
    }

    @Test
    fun `every refusal says something a parent could be shown`() {
        val refusals = listOf(
            TunnelPlan.decide(FilterPolicy(enabled = false), resolvers, 1),
            TunnelPlan.decide(on, resolvers, 1, stoodDown = true),
            TunnelPlan.decide(on, resolvers, 0),
            TunnelPlan.decide(on, emptyList(), 1),
        )

        val reasons = refusals.map { (it as TunnelDecision.Stand).reason }
        // Four states, four distinct explanations. One shared reason would make the console unable
        // to tell a parent which of them they are looking at.
        assertEquals(4, reasons.toSet().size)
        for (reason in reasons) {
            assertTrue("empty reason", reason.isNotBlank())
            assertTrue("$reason reads like an identifier", reason.contains(' '))
        }
    }

    private fun assertStands(decision: TunnelDecision, fragment: String) {
        assertTrue("expected a refusal, got $decision", decision is TunnelDecision.Stand)
        val reason = (decision as TunnelDecision.Stand).reason
        assertTrue("\"$reason\" does not mention \"$fragment\"", reason.contains(fragment))
    }
}
