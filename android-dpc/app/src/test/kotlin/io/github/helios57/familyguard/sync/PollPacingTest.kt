package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The screen-on poll is the only thing that notices time running out, so it must not sleep past the
 * moment it does: every minute it oversleeps is a minute of Bonuszeit spent that was not there, and
 * the child sees a debt (measured on a family phone, 2026-10-25: 26 min earned, 30 spent on a film).
 */
class PollPacingTest {

    private val every = 5 * 60_000L

    private fun next(state: DesiredState?) = PollPacing.nextMillis(state, every)

    @Test
    fun `earned time covering a spent limit is re-checked when it runs out`() = assertEquals(
        3 * 60_000L,
        next(DesiredState(dailyLimitEnabled = true, earnedMinutesLeft = 3, earnedActive = EnforcementEngine.REASON_QUOTA)),
    )

    @Test
    fun `earned time covering bedtime is re-checked when it runs out`() = assertEquals(
        60_000L,
        next(DesiredState(earnedMinutesLeft = 1, earnedActive = EnforcementEngine.REASON_BEDTIME)),
    )

    @Test
    fun `a bonus app spends earned time with the limit not yet reached`() = assertEquals(
        2 * 60_000L,
        next(DesiredState(dailyLimitEnabled = true, remainingMinutes = 40, earnedMinutesLeft = 2, bonusPackages = listOf("b.game"))),
    )

    @Test
    fun `the daily time running out is re-checked when it does`() = assertEquals(
        4 * 60_000L,
        next(DesiredState(dailyLimitEnabled = true, quotaMinutes = 60, remainingMinutes = 4)),
    )

    @Test
    fun `the sooner of the two wins`() = assertEquals(
        2 * 60_000L,
        next(DesiredState(dailyLimitEnabled = true, remainingMinutes = 2, earnedMinutesLeft = 4, bonusPackages = listOf("b.game"))),
    )

    @Test
    fun `more than the cadence left keeps the cadence`() = assertEquals(
        every,
        next(DesiredState(earnedMinutesLeft = 26, earnedActive = EnforcementEngine.REASON_QUOTA)),
    )

    @Test
    fun `nothing left to run out keeps the cadence`() = assertEquals(
        every,
        next(DesiredState(dailyLimitEnabled = true, suspendReason = EnforcementEngine.REASON_QUOTA, earnedMinutesLeft = -4)),
    )

    @Test
    fun `earned time nothing is spending keeps the cadence`() = assertEquals(
        every,
        next(DesiredState(earnedMinutesLeft = 1)),
    )

    @Test
    fun `a pause keeps the cadence`() = assertEquals(
        every,
        next(
            DesiredState(
                dailyLimitEnabled = true,
                remainingMinutes = 1,
                suspendReason = EnforcementEngine.REASON_PAUSED,
                earnedMinutesLeft = 1,
                bonusPackages = listOf("b.game"),
            ),
        ),
    )

    @Test
    fun `no state yet keeps the cadence`() = assertEquals(every, next(null))
}
