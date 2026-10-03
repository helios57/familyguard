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
    /** The limit in force today including [bonusMinutes]; 0 with a [dailyLimitMinutes] is no time left. */
    val limitMinutes: Int,
    /** Today's adjustment: + extra, − taken away (FR-3.11, FR-21). */
    val bonusMinutes: Int,
    /** The plain daily limit, 0 for none — the one number that says whether there is a limit at all. */
    val dailyLimitMinutes: Int,
    /** Earned time left today (FR-22), negative for an overdraft. */
    val earnedMinutesLeft: Int = 0,
    /** BEDTIME or QUOTA while earned time is carrying it, else "". */
    val earnedActive: String = "",
    /** PAUSED, QUOTA or BEDTIME while that pauses every app that is not always free, else "". */
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
        /**
         * False for the home screen, System UI and FamilyGuard itself (FR-3.8), and for an app the
         * daily limit never pauses (FR-5.8).
         */
        val counted: Boolean,
        /** Why it cannot be used right now, or null when it can. */
        val blocked: Block?,
    ) {
        /**
         * What the child is told about the app. An always-free app is named as one — its label says
         * it does not count — rather than as "not counted", which since 0.6.38 it also is: the
         * child should learn WHY a game does not spend the limit, not only that it does not.
         */
        val label: Label
            get() = when {
                rule == Rule.ALWAYS_FREE -> Label.ALWAYS_FREE
                rule == Rule.FREE_PREINSTALLED -> Label.FREE_PREINSTALLED
                !counted -> Label.NOT_COUNTED
                rule == Rule.BONUS -> Label.BONUS
                rule == Rule.BLOCKED_BY_PARENT -> Label.BLOCKED_BY_PARENT
                rule == Rule.OWN_LIMIT -> Label.OWN_LIMIT
                else -> Label.COUNTS
            }
    }

    enum class Label { ALWAYS_FREE, FREE_PREINSTALLED, NOT_COUNTED, BONUS, BLOCKED_BY_PARENT, OWN_LIMIT, COUNTS }

    /** [FREE_PREINSTALLED] is a preinstalled app nobody decided about (FR-5.10). */
    enum class Rule { ALWAYS_FREE, FREE_PREINSTALLED, BONUS, OWN_LIMIT, COUNTS, BLOCKED_BY_PARENT }

    /** The server's reason names (httpapi.BlockedByRule and friends). */
    enum class Block { PAUSED, EARNED, QUOTA, BEDTIME, APP_LIMIT, BLOCKED, PENDING }

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
            val bonus = state.bonusPackages.toSet()

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
                        pkg in bonus -> Rule.BONUS
                        own > 0 -> Rule.OWN_LIMIT
                        else -> Rule.COUNTS
                    },
                    counted = pkg !in uncounted,
                    blocked = blockedReason(
                        pkg, hidden, pending, suspended, own, used, state.suspendReason,
                        bonusWithoutEarnedTime = pkg in bonus && state.earnedMinutesLeft <= 0,
                    ),
                )
            }.sortedWith(compareByDescending<AppLine> { it.usedMinutes }.thenBy { it.packageName })

            return TodayReport(
                usedMinutes = state.usedMinutes,
                limitMinutes = state.quotaMinutes,
                bonusMinutes = state.bonusMinutes,
                dailyLimitMinutes = state.dailyLimitMinutes,
                earnedMinutesLeft = state.earnedMinutesLeft,
                earnedActive = state.earnedActive,
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
            bonusWithoutEarnedTime: Boolean = false,
        ): Block? = when {
            pkg in hidden -> Block.BLOCKED
            pkg in pending -> Block.PENDING
            pkg !in suspended -> null
            ownLimit > 0 && usedMinutes >= ownLimit -> Block.APP_LIMIT
            suspendReason == EnforcementEngine.REASON_PAUSED -> Block.PAUSED
            bonusWithoutEarnedTime -> Block.EARNED
            suspendReason == EnforcementEngine.REASON_QUOTA -> Block.QUOTA
            suspendReason == EnforcementEngine.REASON_BEDTIME -> Block.BEDTIME
            else -> Block.BLOCKED
        }

        /** Foreground time that is not use on every phone (FR-3.8); the home screen is added per phone. */
        const val SYSTEM_UI = "com.android.systemui"
    }
}
