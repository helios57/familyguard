package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.policy.RestrictionGateway
import io.github.helios57.familyguard.policy.RestrictionPlan
import io.github.helios57.familyguard.sync.ApplyOutcome
import io.github.helios57.familyguard.sync.StateApplier

/**
 * FR-31.4: keeping the call-screening role where the parent put it.
 *
 * Android 14 added `disallow_config_default_apps`, which stops the child changing default apps —
 * the call-screening role among them. Below 14 nothing can stop it, so there the restriction is never
 * set and the role check at every sync, reported as [CallGuard.reported], is the whole guard.
 *
 * It is set only on top of a role already held: set before, it would block the dialog that grants
 * the role. It is cleared when the switch goes off, and when the role is lost, so the phone can be
 * asked again. It is deliberately not in `RestrictionPlanner.MANAGED`: the authoritative planner
 * clears every managed key the desired state does not name, and no desired state names this one.
 */
object CallGuardRestriction {
    const val KEY = "disallow_config_default_apps"

    /** The first Android that has [KEY]. */
    const val FIRST_SDK = 34

    fun plan(sdk: Int, switchOn: Boolean, roleHeld: Boolean, current: Set<String>): RestrictionPlan {
        val want = sdk >= FIRST_SDK && switchOn && roleHeld
        val present = KEY in current
        return when {
            want && !present -> RestrictionPlan(add = listOf(KEY), clear = emptyList())
            !want && present -> RestrictionPlan(add = emptyList(), clear = listOf(KEY))
            else -> RestrictionPlan(add = emptyList(), clear = emptyList())
        }
    }
}

object CallGuard {
    /**
     * The heartbeat's `call_screening`: whether the role is held, and nothing while the switch is off.
     * Off, the role means nothing, and reporting false would turn the console's "taken away" red for
     * a phone that is doing exactly what the parent asked.
     */
    fun reported(switchOn: Boolean, roleHeld: Boolean): Boolean? = if (switchOn) roleHeld else null
}

/**
 * Applies [CallGuardRestriction] and shows or withdraws the "Anrufschutz einrichten" prompt, every
 * sync. Its own applier rather than part of the restriction applier, because its input is not in the
 * desired state alone: whether the role is held is a fact about the phone, read now.
 */
class CallGuardApplier(
    private val gateway: RestrictionGateway,
    private val sdk: Int,
    private val roleHeld: () -> Boolean,
    private val setupPrompt: (Boolean) -> Unit,
) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val held = roleHeld()
        setupPrompt(state.callsContactsOnly && !held)
        val plan = CallGuardRestriction.plan(sdk, state.callsContactsOnly, held, gateway.current())
        val problems = LinkedHashMap<String, String>()
        plan.clear.forEach { key -> runCatching { gateway.clear(key) }.onFailure { problems[key] = "clear: ${it.message}" } }
        plan.add.forEach { key -> runCatching { gateway.add(key) }.onFailure { problems[key] = "add: ${it.message}" } }
        // Read back, as the restriction applier does: an accepted request is not an applied one.
        val effective = gateway.current()
        plan.add.filter { it !in effective && it !in problems }.forEach { problems[it] = "requested, accepted, and not in effect" }
        return ApplyOutcome("role=${if (held) "held" else "not held"} added=${plan.add.size} cleared=${plan.clear.size}", problems)
    }
}
