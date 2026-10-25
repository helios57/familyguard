package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine

/**
 * How long the screen-on poll may wait after a tick, given the state that tick enforced.
 *
 * The poll is the only thing that notices time running out — the day's minutes or the Bonuszeit —
 * because nothing else measures between two server events. At a fixed five minutes it noticed late
 * by up to five, and with earned time those minutes are charged: a child with 11 minutes left
 * watched 15 and was shown a debt of 4 (a family phone, 2026-10-25). So the wait is the cadence or
 * the minutes left, whichever is sooner. The minutes are whole and floored, so the tick lands at
 * or just before the real end, never after it.
 *
 * Pure: the loop measures and enforces, this only says when.
 */
object PollPacing {

    fun nextMillis(state: DesiredState?, every: Long): Long {
        if (state == null || state.suspendReason == EnforcementEngine.REASON_PAUSED) return every
        var left = Int.MAX_VALUE
        // FR-22.5: earned time is spent while it covers a spent limit or bedtime, and always by a bonus app.
        val spendingEarned = state.earnedActive.isNotEmpty() || state.bonusPackages.isNotEmpty()
        if (spendingEarned && state.earnedMinutesLeft > 0) left = minOf(left, state.earnedMinutesLeft)
        // The day's own minutes, while they are what the phone runs on.
        val onTheDay = state.dailyLimitEnabled && state.suspendReason.isEmpty() && state.earnedActive.isEmpty()
        if (onTheDay && state.remainingMinutes > 0) left = minOf(left, state.remainingMinutes)
        return if (left == Int.MAX_VALUE) every else minOf(every, left * 60_000L)
    }
}
