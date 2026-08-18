package io.github.helios57.familyguard.policy

/**
 * The platform calls app suspension needs, named so the decisions can be tested without a phone.
 *
 * Every read is a read of what is *in effect*. `setPackagesSuspended` returns the packages it could
 * not act on, and `setApplicationHidden` returns a boolean, so both are already honest about partial
 * failure — but neither says anything about a package that was suspended by an earlier policy and
 * must now be released, which is the half a bedtime that never ends is made of.
 */
interface AppGateway {
    /**
     * Every package installed for this user, system ones included.
     *
     * Used to decide what can be acted on at all: a blocked package that is not installed is a
     * parent blocking something ahead of time, not a failure. The DPC's own package must appear
     * here — [AppSuspensionManager] checks that, because package-visibility filtering would
     * otherwise turn a short list into a silent "nothing to suspend".
     */
    fun installed(): Set<String>

    /** Packages currently suspended, read back from the platform. */
    fun suspended(): Set<String>

    /** Packages currently hidden, read back from the platform. */
    fun hidden(): Set<String>

    /**
     * @return the packages the platform refused, with a reason. An empty map means it took them all.
     */
    fun setSuspended(packages: List<String>, suspended: Boolean): Map<String, String>

    /** @return false when the platform declined. */
    fun setHidden(pkg: String, hidden: Boolean): Boolean
}

/**
 * What one apply changed and what the platform actually reports afterwards.
 *
 * [absent] is not a failure: a parent may block an app before the child ever installs it, and the
 * rule is kept so that installing it later is already covered. It is carried separately from
 * [missing] — which *is* a failure — because collapsing the two would make a phone that cannot
 * suspend anything look identical to a family that blocks apps in advance.
 */
data class AppOutcome(
    val suspended: List<String>,
    val released: List<String>,
    val hidden: List<String>,
    val revealed: List<String>,
    val absent: List<String>,
    val failures: Map<String, String>,
    val missing: Map<String, String>,
    val stillRestrained: List<String>,
) {
    val ok: Boolean get() = failures.isEmpty() && missing.isEmpty() && stillRestrained.isEmpty()

    override fun toString(): String = buildString {
        append("suspended=").append(suspended.size)
        append(" released=").append(released.size)
        append(" hidden=").append(hidden.size)
        append(" revealed=").append(revealed.size)
        if (absent.isNotEmpty()) append(" not-installed=").append(absent.size)
        if (failures.isNotEmpty()) append(" FAILED=").append(failures)
        if (missing.isNotEmpty()) append(" NOT-IN-EFFECT=").append(missing)
        if (stillRestrained.isNotEmpty()) append(" CRITICAL-RESTRAINED=").append(stillRestrained)
    }
}

/** The four lists a single apply carries out, plus the desired packages that are not installed. */
data class AppPlan(
    val suspend: List<String>,
    val release: List<String>,
    val hide: List<String>,
    val reveal: List<String>,
    val absent: List<String>,
)

/**
 * Turns a desired suspension/hiding set into the smallest set of platform calls that converges on it.
 *
 * Pure, and separate from the manager for the same reason [RestrictionPlanner] is: this is where the
 * judgement lives — most of all the judgement about what is never touched — and judgement belongs in
 * a test that runs in milliseconds.
 */
object AppSuspensionPlanner {

    /**
     * @param protectedPackages never suspended and never hidden, whatever the server sent (FR-5.5).
     * The engine already strips these, so this filter normally removes nothing; it is here because
     * the one input this device cannot verify is the server's, and the failure it prevents is a
     * child who cannot dial 112. A protected package that is *currently* restrained is released,
     * which is the half that repairs a device restrained by an older policy.
     */
    fun plan(
        desiredSuspended: Collection<String>,
        desiredHidden: Collection<String>,
        installed: Set<String>,
        currentlySuspended: Set<String>,
        currentlyHidden: Set<String>,
        protectedPackages: Set<String>,
    ): AppPlan {
        val wantSuspended = desiredSuspended.filterTo(sortedSetOf()) {
            it.isNotEmpty() && it !in protectedPackages
        }
        val wantHidden = desiredHidden.filterTo(sortedSetOf()) {
            it.isNotEmpty() && it !in protectedPackages
        }

        val absent = sortedSetOf<String>()
        absent += wantSuspended.filter { it !in installed }
        absent += wantHidden.filter { it !in installed }

        val suspendable = wantSuspended.filterTo(sortedSetOf()) { it in installed }
        val hideable = wantHidden.filterTo(sortedSetOf()) { it in installed }

        return AppPlan(
            suspend = suspendable.filter { it !in currentlySuspended },
            // Released against what was *wanted*, not against what is installed. A package the
            // platform stopped reporting as installed while it is still suspended must not be
            // released just because it went invisible — that would hand the child an app back on
            // the strength of a read that failed.
            release = currentlySuspended.filter { it !in wantSuspended }.sorted(),
            hide = hideable.filter { it !in currentlyHidden },
            reveal = currentlyHidden.filter { it !in wantHidden }.sorted(),
            absent = absent.toList(),
        )
    }
}

/**
 * Applies the suspended/hidden halves of a desired state, then reads back what the platform did.
 *
 * @param protectedPackages the critical whitelist as this device knows it — the engine's built-in
 * list, what [io.github.helios57.familyguard.device.CriticalPackages] found on this hardware, and this app's
 * own package. Passed in rather than read here so the manager stays free of Android imports.
 */
class AppSuspensionManager(
    private val gateway: AppGateway,
    private val protectedPackages: Set<String>,
    private val ownPackage: String,
) {

    fun apply(desiredSuspended: Collection<String>, desiredHidden: Collection<String>): AppOutcome {
        val installed = gateway.installed()
        val failures = LinkedHashMap<String, String>()

        // A positive control on the read itself, not on the policy. Package-visibility filtering
        // turns `installed()` into a short list rather than an error, and a short list means every
        // blocked app is quietly reported as "not installed" — a phone that suspends nothing while
        // every sync reports clean. The DPC can always see itself, so its absence is proof the read
        // is not measuring the device.
        if (ownPackage.isNotEmpty() && ownPackage !in installed) {
            failures["!installed"] =
                "the installed-package read does not contain this app, so it cannot be trusted"
        }

        val plan = AppSuspensionPlanner.plan(
            desiredSuspended = desiredSuspended,
            desiredHidden = desiredHidden,
            installed = installed,
            currentlySuspended = gateway.suspended(),
            currentlyHidden = gateway.hidden(),
            protectedPackages = protectedPackages,
        )

        // Releases before restraints, for the reason HardeningManager clears before it adds: if this
        // process is killed halfway through — a low-memory kill during a bedtime transition — the
        // ordering decides whether the phone is left more restrained than intended or less. Here the
        // stake is concrete: a dialer that an older policy suspended is freed before anything else
        // is suspended.
        carry(plan.release, suspended = false, failures)
        carry(plan.suspend, suspended = true, failures)
        for (pkg in plan.reveal) hide(pkg, hidden = false, failures)
        for (pkg in plan.hide) hide(pkg, hidden = true, failures)

        // The authority is the platform. `setPackagesSuspended` returning an empty array says the
        // request was accepted, not that it took effect.
        val effectiveSuspended = gateway.suspended()
        val effectiveHidden = gateway.hidden()

        val missing = LinkedHashMap<String, String>()
        desiredSuspended
            .filter { it.isNotEmpty() && it !in protectedPackages && it in installed }
            .sorted()
            .filter { it !in effectiveSuspended }
            .forEach { missing[it] = "suspension requested, accepted, and not in effect" }
        desiredHidden
            .filter { it.isNotEmpty() && it !in protectedPackages && it in installed }
            .sorted()
            .filter { it !in effectiveHidden }
            // Two reasons for one package is one line, and the specific half is the suspension: a
            // hidden-but-running app is a cosmetic failure, a visible-and-usable one is not.
            .forEach { missing.putIfAbsent(it, "hiding requested, accepted, and not in effect") }

        return AppOutcome(
            suspended = plan.suspend,
            released = plan.release,
            hidden = plan.hide,
            revealed = plan.reveal,
            absent = plan.absent,
            failures = failures,
            missing = missing,
            // FR-5.5 at the layer that reports. Nothing above asks for these, so reaching this means
            // something else on the device restrained them — and a suspended dialer is the one
            // failure this project treats as unacceptable rather than merely bad.
            stillRestrained = protectedPackages
                .filter { it in effectiveSuspended || it in effectiveHidden }
                .sorted(),
        )
    }

    /**
     * One call for the whole batch, because `setPackagesSuspended` is atomic per call in the sense
     * that matters: it reports which names it refused, so a single refusal does not cost the rest.
     */
    private fun carry(packages: List<String>, suspended: Boolean, failures: MutableMap<String, String>) {
        if (packages.isEmpty()) return
        val verb = if (suspended) "suspend" else "release"
        try {
            gateway.setSuspended(packages, suspended).forEach { (pkg, reason) ->
                failures[pkg] = "$verb: $reason"
            }
        } catch (e: RuntimeException) {
            // One exception for the batch names no package, so every package in it is charged. A
            // batch that vanished silently would be indistinguishable from one that worked.
            val reason = e.message ?: e.javaClass.simpleName
            packages.forEach { failures[it] = "$verb: $reason" }
        }
    }

    private fun hide(pkg: String, hidden: Boolean, failures: MutableMap<String, String>) {
        val verb = if (hidden) "hide" else "reveal"
        try {
            if (!gateway.setHidden(pkg, hidden)) failures[pkg] = "$verb: the platform declined"
        } catch (e: RuntimeException) {
            failures[pkg] = "$verb: " + (e.message ?: e.javaClass.simpleName)
        }
    }
}
