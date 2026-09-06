package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.ManagedApp
import io.github.helios57.familyguard.policy.HardeningManager
import io.github.helios57.familyguard.update.UpdateOutcome

/**
 * Makes the phone hold exactly the applications the parent declared (FR-16.3, FR-16.5).
 *
 * A **set**, converged on at every sync, not a queue of install commands. That is the whole shape
 * of the feature: an install that failed on a phone with no storage retries by itself when storage
 * frees up, and an application a child managed to remove comes back — without the parent having to
 * notice anything went wrong and press something again.
 *
 * **What may be removed is bounded by what this app installed.** The declared set says what should
 * be present; it says nothing about the thousand other packages on the phone, and an applier that
 * uninstalled everything not in it would wipe the device on its first pass. [installedByThisApp] is
 * the platform's own record of which packages this app is the installer of, so the removal
 * candidates are exactly the ones a previous pass put there.
 *
 * **The restriction window is as narrow as the platform allows.** `no_install_apps` and
 * `no_uninstall_apps` are set on the user this app runs as, so they bind the device owner too, and
 * the operation cannot proceed while they are in effect. Every APK is therefore downloaded and
 * verified *first*, outside the window; [HardeningManager.withoutRestrictions] is then opened
 * around nothing but the installer sessions and the uninstalls. A 30 MB download on a phone's
 * connection is minutes with installs allowed; a local copy into a session is seconds.
 */
class ManagedAppApplier(
    private val hardening: HardeningManager,
    private val installedVersion: (packageName: String) -> Long?,
    private val installedByThisApp: () -> Set<String>,
    private val stage: (ManagedApp) -> UpdateOutcome,
    private val uninstall: (packageName: String) -> String?,
    private val log: (String) -> Unit = {},
) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val declared = state.managedApps
        val problems = LinkedHashMap<String, String>()

        // The cheap comparison, and the reason a converged phone does not download anything. It is
        // done here rather than inside the updater, which discovers the version only after the
        // download: correct for a self-update, which is one command a parent pressed, and
        // ruinous for a set re-evaluated on every sync.
        val wanted = declared.filter { installedVersion(it.packageName) != it.versionCode }
        val keep = declared.mapTo(mutableSetOf()) { it.packageName }
        val remove = installedByThisApp().filterTo(sortedSetOf()) { it !in keep }

        if (wanted.isEmpty() && remove.isEmpty()) {
            return ApplyOutcome("declared=${declared.size} converged")
        }

        // Staged outside the window. A refusal here is reported against the package and does not
        // stop the others: one application whose checksum does not match must not keep the rest of
        // a child's declared set off the phone.
        val staged = mutableListOf<Pair<ManagedApp, UpdateOutcome.Staged>>()
        for (app in wanted) {
            when (val outcome = stage(app)) {
                is UpdateOutcome.Staged -> staged += app to outcome
                // The updater found the phone already running this build. Reachable when the
                // package manager and the version this applier read disagree, which is a
                // convergence question and not a problem to report to a parent.
                is UpdateOutcome.AlreadyCurrent -> log("${app.packageName} was already current")
                is UpdateOutcome.Refused -> problems[app.packageName] = outcome.reason
            }
        }

        if (staged.isEmpty() && remove.isEmpty()) {
            return ApplyOutcome("declared=${declared.size} staged=0 removed=0", problems)
        }

        var installed = 0
        var removed = 0
        hardening.withoutRestrictions(LIFTED) {
            for ((app, ready) in staged) {
                // The updater hands the whole platform half back rather than doing any of it, so
                // that all of it — the session, the write and the commit — happens inside the
                // window. The download that preceded it did not, which is the point.
                val failure = runCatching { ready.commit() }.exceptionOrNull()
                    ?.let { it.message ?: it.javaClass.simpleName }
                if (failure == null) installed++ else problems[app.packageName] = failure
            }
            for (packageName in remove) {
                val failure = uninstall(packageName)
                if (failure == null) removed++ else problems[packageName] = failure
            }
        }

        log("managed apps: declared=${declared.size} installed=$installed removed=$removed problems=${problems.size}")
        return ApplyOutcome(
            summary = "declared=${declared.size} installed=$installed removed=$removed",
            problems = problems,
        )
    }

    companion object {
        /**
         * The two restrictions that stop a device owner adding or removing a package.
         *
         * `no_install_apps` is documented by the platform as preventing "device owners and profile
         * owners installing apps" — it binds the app that set it, which is what makes this window
         * necessary rather than defensive.
         *
         * `no_install_unknown_sources` is deliberately NOT here. It governs installs from a source
         * the platform does not trust, and a `PackageInstaller` session opened by the device owner
         * is not one — lifting it would weaken the phone for the benefit of an operation it does
         * not bind.
         *
         * Public because the **self-update** needs the identical window (FR-15). It was missing
         * there: a family with "let this child install apps" switched off had `no_install_apps` in
         * effect, and every self-update would have been refused by `createSession` with no session
         * ever opened. See `ConnectionService.selfUpdater`.
         */
        val LIFTED = listOf(
            EnforcementEngine.RESTRICTION_INSTALL_APPS,
            EnforcementEngine.RESTRICTION_UNINSTALL_APPS,
        )
    }
}
