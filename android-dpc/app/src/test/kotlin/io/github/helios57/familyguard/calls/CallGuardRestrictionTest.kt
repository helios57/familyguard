package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.policy.RestrictionGateway
import io.github.helios57.familyguard.policy.RestrictionPlan
import io.github.helios57.familyguard.policy.RestrictionPlanner
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-31.4: once the phone holds the call-screening role, Android 14 and up can stop the child taking
 * it away (`disallow_config_default_apps`). Below 14 nothing can, so the restriction is never set
 * there and the sync's role check is the only guard. The restriction is set only on top of a held
 * role: set before, it would block the very dialog that grants it.
 */
class CallGuardRestrictionTest {

    private val key = CallGuardRestriction.KEY

    @Test
    fun `held, on and Android 14 adds the restriction`() {
        assertEquals(RestrictionPlan(listOf(key), emptyList()), CallGuardRestriction.plan(34, true, true, emptySet()))
    }

    @Test
    fun `not held yet adds nothing, so the grant dialog stays reachable`() {
        assertTrue(CallGuardRestriction.plan(36, switchOn = true, roleHeld = false, current = emptySet()).isEmpty)
    }

    @Test
    fun `below Android 14 nothing is ever added`() {
        assertTrue(CallGuardRestriction.plan(33, true, true, emptySet()).isEmpty)
    }

    @Test
    fun `switched off clears it`() {
        assertEquals(RestrictionPlan(emptyList(), listOf(key)), CallGuardRestriction.plan(36, false, true, setOf(key)))
    }

    @Test
    fun `role lost clears it, so the phone can be asked again`() {
        assertEquals(RestrictionPlan(emptyList(), listOf(key)), CallGuardRestriction.plan(36, true, false, setOf(key)))
    }

    @Test
    fun `already in place is left alone`() {
        assertTrue(CallGuardRestriction.plan(36, true, true, setOf(key)).isEmpty)
    }

    @Test
    fun `the authoritative planner never touches it`() {
        assertFalse(key in RestrictionPlanner.MANAGED)
        assertTrue(RestrictionPlanner.plan(setOf(key), emptyList()).isEmpty)
    }

    private class Gateway(var keys: MutableSet<String> = mutableSetOf()) : RestrictionGateway {
        override fun current() = keys.toSet()
        override fun add(key: String) { keys += key }
        override fun clear(key: String) { keys -= key }
    }

    @Test
    fun `the applier sets the restriction and drops the setup prompt once the role is held`() {
        val gateway = Gateway()
        var prompt: Boolean? = null
        val applier = CallGuardApplier(gateway, sdk = 36, roleHeld = { true }, setupPrompt = { prompt = it })
        val outcome = applier.apply(DesiredState(callsContactsOnly = true))
        assertTrue(outcome.ok)
        assertEquals(setOf(key), gateway.keys)
        assertEquals(false, prompt)
    }

    @Test
    fun `the applier prompts while the switch is on and the role is not held`() {
        var prompt: Boolean? = null
        CallGuardApplier(Gateway(), 33, { false }, { prompt = it }).apply(DesiredState(callsContactsOnly = true))
        assertEquals(true, prompt)
        CallGuardApplier(Gateway(), 33, { false }, { prompt = it }).apply(DesiredState(callsContactsOnly = false))
        assertEquals(false, prompt)
    }

    @Test
    fun `the heartbeat says nothing about the role while the switch is off`() {
        assertNull(CallGuard.reported(switchOn = false, roleHeld = true))
        assertEquals(true, CallGuard.reported(true, true))
        assertEquals(false, CallGuard.reported(true, false))
    }
}
