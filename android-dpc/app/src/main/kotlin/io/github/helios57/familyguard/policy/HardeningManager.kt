package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.enforce.EnforcementEngine

/**
 * What actually happened, read back from the platform rather than assumed from the calls that were
 * made.
 *
 * @param added restrictions this run asked the platform to set.
 * @param cleared restrictions this run asked the platform to drop.
 * @param failures the calls that threw, by restriction, with the exception's message.
 * @param stillForbidden forbidden restrictions still in effect after the run — an un-wipeable phone.
 * @param missing restrictions that were requested, did not throw, and are still not in effect.
 * @param clock what the clock enforcement did (FR-2.2), or null on the paths that do not touch it.
 *   Null is *not* a pass: [apply] never touches the clock, and reporting a success it did not
 *   perform would let a sync overwrite a baseline's genuine clock failure with a clean outcome.
 */
data class HardeningOutcome(
    val added: List<String>,
    val cleared: List<String>,
    val failures: Map<String, String>,
    val stillForbidden: List<String>,
    val missing: List<String>,
    val clock: ClockOutcome? = null,
) {
    val ok: Boolean
        get() = failures.isEmpty() && stillForbidden.isEmpty() && missing.isEmpty() &&
            clock?.ok != false

    /** A one-line summary for the log and, later, for the heartbeat. Never silent about a failure. */
    override fun toString(): String = buildString {
        append("added=").append(added).append(" cleared=").append(cleared)
        if (clock != null) append(" ").append(clock)
        if (failures.isNotEmpty()) append(" FAILED=").append(failures)
        if (missing.isNotEmpty()) append(" NOT-IN-EFFECT=").append(missing)
        if (stillForbidden.isNotEmpty()) append(" STILL-FORBIDDEN=").append(stillForbidden)
    }
}

/**
 * Applies a set of user restrictions to the device and then checks that it worked.
 *
 * The decision of *what* to change belongs to [RestrictionPlanner]; this class is the loop that
 * carries it out, plus the read-back that says whether the platform agreed. Splitting it that way is
 * what lets the whole of the judgement be covered by tests that run on the JVM in milliseconds.
 */
class HardeningManager(
    private val gateway: RestrictionGateway,
    /**
     * The clock half of the baseline (FR-2.2).
     *
     * Required rather than defaulted to "no clock enforcement", for the reason
     * `Synchronizer.recovery` is required: the default that makes a call site compile is also the
     * one that silently drops half of FR-2. A phone wired without it would harden every restriction,
     * report a clean baseline, and leave a clock the child can set back an hour every evening —
     * with nothing anywhere red, because a quota that is never reached looks exactly like a child
     * who stopped using their phone.
     */
    private val clock: ClockPolicyManager,
) {

    /**
     * The floor that holds before the server has ever been reached: at provisioning compliance, and
     * on **every** boot.
     *
     * It is [EnforcementEngine.BASELINE_RESTRICTIONS] rather than a list written here, because a
     * second list is a second policy that nobody remembers to update.
     *
     * It goes through [RestrictionPlanner.floor], not [RestrictionPlanner.plan], and the difference
     * is the whole reason that method exists: a floor adds and never takes away, except for a
     * forbidden restriction. Applying the baseline as a full desired state would mean a phone that
     * has been syncing for months comes back from a reboot with every stronger restriction cleared
     * until the next successful sync — a window the child can open on demand.
     */
    fun applyBaseline(): HardeningOutcome = carryOut(
        RestrictionPlanner.floor(gateway.current(), EnforcementEngine.BASELINE_RESTRICTIONS),
        EnforcementEngine.BASELINE_RESTRICTIONS,
        // Automatic network time is the other half of `DISALLOW_CONFIG_DATE_TIME` (FR-2.2), and it
        // belongs here rather than in a caller because both call sites — provisioning compliance and
        // the boot receiver — are exactly the two moments FR-2 names. A second branch at each of them
        // is a second place to forget.
        clock = clock.apply(),
    )

    /**
     * @param desired the `user_restrictions` of a desired state the server sent.
     *
     * Authoritative, unlike [applyBaseline]: a managed restriction the server did not ask for is
     * cleared. This is the only path that may weaken the device, and it runs only when something
     * told us to.
     *
     * It leaves the clock alone, and [HardeningOutcome.clock] is therefore null rather than a
     * success. Automatic time is not a user restriction, so no desired state can carry it and a
     * write here would be a settings-provider call every fifteen minutes for a value only an admin
     * can change. The baseline asserts it at the two moments FR-2 names, and that is enough.
     */
    fun apply(desired: Collection<String>): HardeningOutcome =
        carryOut(RestrictionPlanner.plan(gateway.current(), desired), desired)

    /**
     * Clears run before adds. Not for efficiency — the two sets are disjoint by construction — but
     * because `clear` is the half that restores the escape hatch. If this process is killed halfway
     * through (a provisioning flow the user backs out of, a boot receiver that runs out of time),
     * the ordering decides whether the phone is left more locked down than intended or less.
     *
     * A call that throws does not abort the rest. An OEM that rejects one restriction must not cost
     * the device the other six, and the caller is told exactly which one failed rather than being
     * handed an exception that names only the first.
     */
    private fun carryOut(
        plan: RestrictionPlan,
        requested: Collection<String>,
        clock: ClockOutcome? = null,
    ): HardeningOutcome {
        val failures = LinkedHashMap<String, String>()

        for (key in plan.clear) {
            try {
                gateway.clear(key)
            } catch (e: RuntimeException) {
                failures[key] = "clear: " + (e.message ?: e.javaClass.simpleName)
            }
        }
        for (key in plan.add) {
            try {
                gateway.add(key)
            } catch (e: RuntimeException) {
                failures[key] = "add: " + (e.message ?: e.javaClass.simpleName)
            }
        }

        // The authority is the platform, not this method's own bookkeeping. `addUserRestriction` is
        // a request, and a request that is accepted and never applied is exactly the shape of a
        // device that reports itself hardened while enforcing nothing.
        val effective = gateway.current()
        val want = requested.filter { it.isNotEmpty() && it !in EnforcementEngine.FORBIDDEN_RESTRICTIONS }
        return HardeningOutcome(
            added = plan.add,
            cleared = plan.clear,
            failures = failures,
            stillForbidden = EnforcementEngine.FORBIDDEN_RESTRICTIONS.filter { it in effective },
            missing = want.filter { it !in effective }.sorted(),
            clock = clock,
        )
    }
}
