package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.device.AppRestraint

/**
 * The platform calls app suspension needs, named so the decisions can be tested without a phone.
 *
 * Every read is a read of what is *in effect*. `setPackagesSuspended` returns the packages it could
 * not act on, and `setApplicationHidden` returns a boolean, so both are already honest about partial
 * failure — but neither says anything about a package that was suspended by an earlier policy and
 * must now be released, which is the half a bedtime that never ends is made of.
 */
interface AppGateway : AppRestraint {
    /**
     * Every package this DPC can act on for this user: installed, system ones included, **plus the
     * ones it has hidden**.
     *
     * Used to decide what can be acted on at all: a blocked package that is not installed is a
     * parent blocking something ahead of time, not a failure. The DPC's own package must appear
     * here — [AppSuspensionManager] checks that, because package-visibility filtering would
     * otherwise turn a short list into a silent "nothing to suspend".
     *
     * Hidden packages are part of the contract rather than an implementation detail of one
     * gateway, because reversibility rests on it: everything downstream is filtered by this set,
     * so an implementation that drops what it has hidden can never reveal it again.
     */
    fun installed(): Set<String>

    /** Packages currently suspended, read back from the platform. A subset of [installed]. */
    override fun suspended(): Set<String>

    /** Packages currently hidden, read back from the platform. A subset of [installed]. */
    override fun hidden(): Set<String>

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
 * The set an [AppGateway] must report from two platform reads, one narrow and one wide.
 *
 * Pure, and separate from the gateway for the reason [AppSuspensionPlanner] is separate from the
 * manager: the decision is testable in milliseconds and the binder calls around it are not.
 */
object InstalledPackages {

    /**
     * Everything this DPC can still act on: what is installed, plus what it has hidden.
     *
     * Hiding clears a package's installed-for-this-user flag, so the narrow read stops returning
     * it. Since every downstream decision is filtered by this set, a result built from the narrow
     * read alone can never reveal what it hid — the app is gone for good, silently, because the
     * sync that should have brought it back reports clean.
     *
     * @param present the narrow read: installed for this user.
     * @param known the wide read: the above plus packages the platform merely remembers. Treated
     * as a candidate list, never as an answer — it also returns apps that are genuinely gone with
     * only their data retained, and asking the platform to hide one of those is a refusal that
     * would then be reported as a failure.
     * @param isHidden asked only about the difference between the two reads, which is the only
     * place the answer can change the result.
     */
    fun union(
        present: Collection<String>,
        known: Collection<String>,
        isHidden: (String) -> Boolean,
    ): Set<String> {
        val out = present.toMutableSet()
        for (pkg in known) {
            if (pkg in out) continue
            if (isHidden(pkg)) out += pkg
        }
        return out
    }
}

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

        // Hiding subsumes suspension, so a package on both lists is only hidden.
        //
        // Not an optimisation. `setApplicationHidden` clears the package's installed-for-this-user
        // flag, after which the platform answers `isPackageSuspended` for it with
        // `NameNotFoundException` — read here as "not suspended". Asking for both would therefore
        // re-request the suspension on every sync for the rest of the phone's life, have it
        // refused, and record a failure each time: red forever, for every app the family blocks,
        // which is the state that teaches a parent to skip the whole report. A hidden app cannot
        // be launched; there is nothing left for suspension to add.
        val suspendable = wantSuspended.filterTo(sortedSetOf()) { it in installed && it !in wantHidden }
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
    /**
     * Public because the inventory reader needs the same answers this manager acts on. Two readers
     * would be two opinions about what is hidden, and the console's copy would drift from the one
     * the phone is enforcing.
     */
    val gateway: AppGateway,
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

        // Checked against what was actually asked of the platform, which is the planner's rule
        // mirrored: a package on both lists is only hidden, so charging it with a missing
        // suspension would report a failure for a call this code deliberately never made — and
        // would do it on every sync, for every app the family blocks. Still one line per package:
        // two reasons for one app is one problem.
        val actionable = { pkg: String -> pkg.isNotEmpty() && pkg !in protectedPackages && pkg in installed }
        val wantHidden = desiredHidden.filterTo(sortedSetOf(), actionable)
        val wantSuspended = desiredSuspended.filterTo(sortedSetOf(), actionable)

        val missing = LinkedHashMap<String, String>()
        for (pkg in wantHidden) {
            if (pkg !in effectiveHidden) missing[pkg] = "hiding requested, accepted, and not in effect"
        }
        for (pkg in wantSuspended) {
            if (pkg in wantHidden) continue
            if (pkg !in effectiveSuspended) {
                missing[pkg] = "suspension requested, accepted, and not in effect"
            }
        }

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
