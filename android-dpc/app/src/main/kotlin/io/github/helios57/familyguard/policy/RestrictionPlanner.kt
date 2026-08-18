package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.enforce.EnforcementEngine

/**
 * What to change about the device's user restrictions: nothing else, and never more than this.
 *
 * The decision lives here, away from Android, because it is the one place in the DPC that can brick
 * a phone. `HardeningManager` is then a loop over two lists with no judgement in it, and every
 * judgement is covered by a JVM test that runs in milliseconds.
 */
data class RestrictionPlan(val add: List<String>, val clear: List<String>) {
    val isEmpty: Boolean get() = add.isEmpty() && clear.isEmpty()
}

object RestrictionPlanner {

    /**
     * Every restriction this DPC is willing to touch. A restriction outside this set is left exactly
     * as it is, whoever set it: the device is ours to manage, but silently clearing something we
     * never set — an OEM's, or a control we removed from this list last release — is a change nobody
     * asked for and nobody would see.
     */
    val MANAGED: Set<String> = setOf(
        EnforcementEngine.RESTRICTION_SAFE_BOOT,
        EnforcementEngine.RESTRICTION_DEBUGGING,
        EnforcementEngine.RESTRICTION_PRIVATE_DNS,
        EnforcementEngine.RESTRICTION_DATE_TIME,
        EnforcementEngine.RESTRICTION_ADD_USER,
        EnforcementEngine.RESTRICTION_UNKNOWN_SOURCES,
        EnforcementEngine.RESTRICTION_INSTALL_APPS,
        EnforcementEngine.RESTRICTION_UNINSTALL_APPS,
    ) + EnforcementEngine.FORBIDDEN_RESTRICTIONS

    /**
     * @param current what `UserManager.getUserRestrictions()` reports is in effect now.
     * @param desired the `user_restrictions` list from the desired state.
     *
     * The forbidden set — `no_factory_reset` above all — is filtered out of [desired] here as well
     * as on the server, and that duplication is the point. The server already refuses to send it, so
     * this filter is unreachable through the product's own control plane; it exists for the case
     * where the control plane is not the product's own. A DPC that applies whatever JSON it is
     * handed makes a bricked phone one bad response away, and the phone is the thing that cannot be
     * rolled back.
     *
     * Because [MANAGED] contains the forbidden names and [desired] never can, a forbidden
     * restriction that is somehow already in effect always lands in `clear`. Nothing this app does
     * can leave the device un-wipeable (FR-2.3, NFR-6).
     */
    fun plan(current: Set<String>, desired: Collection<String>): RestrictionPlan {
        val want = desired
            .filter { it.isNotEmpty() && it !in EnforcementEngine.FORBIDDEN_RESTRICTIONS }
            .toSortedSet()
        val add = want.filter { it !in current }
        val clear = MANAGED.filter { it in current && it !in want }.sorted()
        return RestrictionPlan(add = add, clear = clear)
    }

    /**
     * A **floor**, for the paths that run before the server has been reached: provisioning
     * compliance, and every boot.
     *
     * The difference from [plan] is that this one never takes anything away except a forbidden
     * restriction. [plan] treats [desired] as the whole truth and clears any managed restriction not
     * in it, which is right when the server just told us what it wants and wrong at boot — a phone
     * that has been syncing for months reboots, applies the pre-sync floor, and would sit with every
     * stronger restriction *cleared* until the next successful sync. That is a window a child can
     * create on demand by rebooting the phone, and nothing about the device would look wrong during
     * it.
     *
     * The forbidden set is still cleared, because that is the half that restores an escape hatch
     * rather than removing one: FR-2.3 says a boot must leave the device wipeable, whatever put it
     * in the state it booted into.
     */
    fun floor(current: Set<String>, required: Collection<String>): RestrictionPlan {
        val want = required
            .filter { it.isNotEmpty() && it !in EnforcementEngine.FORBIDDEN_RESTRICTIONS }
            .toSortedSet()
        val add = want.filter { it !in current }
        val clear = EnforcementEngine.FORBIDDEN_RESTRICTIONS.filter { it in current }.sorted()
        return RestrictionPlan(add = add, clear = clear)
    }
}
