package io.github.helios57.familyguard.enforce

/**
 * What a child's phone says about its own day (FR-3.10): how much screen time is used against what
 * limit, and for each app why it can or cannot be used right now.
 *
 * Asked for by the owner on 2026-09-23 — *"it should be shown on the phone … why some applications
 * are not usable any more and how long each application has been used"* — after the phone had
 * locked every app with nobody in front of it, and nothing on the phone said why.
 *
 * Pure: built from the input the engine ran on and the state it produced, so the reasons shown are
 * by construction the reasons in force. The server's console derives the same reasons from the
 * same engine output (httpapi.blockedReason); the names below are that list, and the two must not
 * drift.
 */
data class TodayReport(
    /** Counted screen time today, in whole minutes — the number the limit is compared against. */
    val usedMinutes: Int,
    /** The limit in force today including [bonusMinutes], or 0 when there is no daily limit. */
    val limitMinutes: Int,
    val bonusMinutes: Int,
    /** QUOTA or BEDTIME while that pauses every app that is not always free, else "". */
    val suspendReason: String,
    /** When [suspendReason] ends, RFC 3339, or "". */
    val nextChangeAt: String,
    /** Apps with any use today or a reason they cannot be used, most used first. */
    val apps: List<AppLine>,
) {
    data class AppLine(
        val packageName: String,
        val usedMinutes: Int,
        /** This app's own daily allowance, 0 when it has none. */
        val ownLimitMinutes: Int,
        val rule: Rule,
        /** False for the home screen, System UI and FamilyGuard itself (FR-3.8). */
        val counted: Boolean,
        /** Why it cannot be used right now, or null when it can. */
        val blocked: Block?,
    )

    /** [FREE_PREINSTALLED] is a preinstalled app nobody decided about (FR-5.10). */
    enum class Rule { ALWAYS_FREE, FREE_PREINSTALLED, OWN_LIMIT, COUNTS, BLOCKED_BY_PARENT }

    /** The server's reason names (httpapi.BlockedByRule and friends). */
    enum class Block { QUOTA, BEDTIME, APP_LIMIT, BLOCKED, PENDING }

    companion object {
        fun of(
            input: Input,
            state: DesiredState,
            usedByPackage: Map<String, Int>,
            uncounted: Set<String>,
        ): TodayReport {
            val allowed = input.settings.allowedPackages.toSet()
            val parentBlocked = input.settings.blockedPackages.toSet() +
                (if (input.settings.youtubeBlocked) EnforcementEngine.YOUTUBE_PACKAGES else emptyList()) +
                input.settings.familyBlockedPackages.filter { it !in allowed }
            val ownLimits = input.settings.limitedPackages.filter { it.minutes > 0 }
                .associate { it.packageName to it.minutes }
            val hidden = state.hiddenPackages.toSet()
            val pending = state.pendingApproval.toSet()
            val suspended = state.suspendedPackages.toSet()
            val freeByDefault = state.freeByDefault.toSet()

            val packages = usedByPackage.filterValues { it > 0 }.keys + pending
            val lines = packages.map { pkg ->
                val used = usedByPackage[pkg] ?: 0
                val own = ownLimits[pkg] ?: 0
                AppLine(
                    packageName = pkg,
                    usedMinutes = used,
                    ownLimitMinutes = own,
                    rule = when {
                        pkg in parentBlocked -> Rule.BLOCKED_BY_PARENT
                        pkg in allowed -> Rule.ALWAYS_FREE
                        pkg in freeByDefault -> Rule.FREE_PREINSTALLED
                        own > 0 -> Rule.OWN_LIMIT
                        else -> Rule.COUNTS
                    },
                    counted = pkg !in uncounted,
                    blocked = blockedReason(pkg, hidden, pending, suspended, own, used, state.suspendReason),
                )
            }.sortedWith(compareByDescending<AppLine> { it.usedMinutes }.thenBy { it.packageName })

            return TodayReport(
                usedMinutes = state.usedMinutes,
                limitMinutes = state.quotaMinutes,
                bonusMinutes = state.bonusMinutes,
                suspendReason = state.suspendReason,
                nextChangeAt = if (state.suspendReason.isEmpty()) "" else state.nextChangeAt,
                apps = lines,
            )
        }

        /** Mirrors httpapi.blockedReason line for line. */
        fun blockedReason(
            pkg: String,
            hidden: Set<String>,
            pending: Set<String>,
            suspended: Set<String>,
            ownLimit: Int,
            usedMinutes: Int,
            suspendReason: String,
        ): Block? = when {
            pkg in hidden -> Block.BLOCKED
            pkg in pending -> Block.PENDING
            pkg !in suspended -> null
            ownLimit > 0 && usedMinutes >= ownLimit -> Block.APP_LIMIT
            suspendReason == EnforcementEngine.REASON_QUOTA -> Block.QUOTA
            suspendReason == EnforcementEngine.REASON_BEDTIME -> Block.BEDTIME
            else -> Block.BLOCKED
        }

        /** Foreground time that is not use on every phone (FR-3.8); the home screen is added per phone. */
        const val SYSTEM_UI = "com.android.systemui"
    }
}
