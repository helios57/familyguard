package io.github.helios57.familyguard.enforce

/**
 * What the phone needs to know, for one measured window, to say which of its minutes were earned
 * time (FR-22). Built from the input and the state the engine last computed — the state in force
 * while the window was being used.
 */
data class EarnedContext(
    /** The policy day this context was computed for; a window on another day is not charged by it. */
    val day: String,
    /** Apps that run only on earned time. */
    val bonus: Set<String>,
    /** Apps that never cost earned time: always free, preinstalled-free, critical, always usable. */
    val exempt: Set<String>,
    /** Foreground time that is not use at all (FR-3.8). */
    val uncounted: Set<String>,
    val inBedtime: Boolean,
    val paused: Boolean,
    /** What is left of today's budget, in ms; [Long.MAX_VALUE] with no daily limit. */
    val budgetLeftMs: Long,
    /**
     * Whether earned time is being spent at all: there is some left, and the phone enforces. False
     * in watch-only mode and with none left — then nothing is charged, because nothing could pay,
     * and a charge would only become a debt against minutes the child has not yet earned.
     */
    val spending: Boolean = true,
)

/**
 * Which of a window's minutes are earned time (Bonuszeit) — FR-22.5.
 *
 * A minute is earned time when it is a bonus app, inside bedtime, or past the daily budget, and
 * never when the app is exempt or uncounted. That is a statement about WHEN a minute was used, so it
 * is decided here, window by window, as the phone measures — not from day totals, where a child who
 * spent the budget on a game in the morning would pay gold for chatting on WhatsApp in the evening.
 *
 * Pure. The window is at most one poll long (~5 minutes), which bounds how far the order of apps
 * inside it can matter.
 */
object EarnedAttribution {

    fun attribute(window: Map<String, Long>, ctx: EarnedContext): Map<String, Long> {
        // Paused, nothing that costs earned time can run: what runs is exempt.
        if (ctx.paused || !ctx.spending) return emptyMap()
        val gold = LinkedHashMap<String, Long>()
        val governed = LinkedHashMap<String, Long>()
        var counted = 0L
        for ((pkg, raw) in window) {
            val ms = raw.coerceAtLeast(0)
            if (ms == 0L || pkg in ctx.uncounted) continue
            if (pkg in ctx.bonus) {
                gold[pkg] = ms
                continue
            }
            counted += ms
            if (pkg !in ctx.exempt) governed[pkg] = ms
        }
        if (ctx.inBedtime) {
            gold.putAll(governed)
            return gold
        }
        val governedTotal = governed.values.sum()
        if (ctx.budgetLeftMs == Long.MAX_VALUE || governedTotal == 0L) return gold
        val overflow = (counted - ctx.budgetLeftMs.coerceAtLeast(0)).coerceAtLeast(0)
        val charged = minOf(overflow, governedTotal)
        if (charged == 0L) return gold
        for ((pkg, ms) in governed) {
            val share = ms * charged / governedTotal
            if (share > 0) gold[pkg] = share
        }
        return gold
    }

    /**
     * The context the engine's last state implies. [uncounted] is the phone's own set (FR-3.8), the
     * same one its day count leaves out.
     */
    fun contextOf(input: Input, state: DesiredState, uncounted: Set<String>, day: String): EarnedContext {
        val exempt = input.settings.allowedPackages.toSet() + state.freeByDefault +
            EnforcementEngine.DEFAULT_CRITICAL_PACKAGES + EnforcementEngine.ALWAYS_USABLE_PACKAGES +
            input.criticalPackages
        val budgetLeft = if (state.dailyLimitMinutes > 0) {
            (state.quotaMinutes - state.usedMinutes).coerceAtLeast(0).toLong() * 60_000L
        } else {
            Long.MAX_VALUE
        }
        return EarnedContext(
            day = day,
            bonus = state.bonusPackages.toSet(),
            exempt = exempt,
            uncounted = uncounted,
            inBedtime = state.suspendReason == EnforcementEngine.REASON_BEDTIME ||
                state.earnedActive == EnforcementEngine.REASON_BEDTIME,
            paused = state.suspendReason == EnforcementEngine.REASON_PAUSED,
            budgetLeftMs = budgetLeft,
            spending = !input.settings.trackingOnly && state.earnedMinutesLeft > 0,
        )
    }
}
