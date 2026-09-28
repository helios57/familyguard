package io.github.helios57.familyguard.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * FR-26.1: the phone keeps its connection open only while someone can benefit from it — the screen is
 * on, it went off less than a minute ago, or Live runs. Everything else is PASSIVE.
 */
class PowerModeTest {

    private val now = 1_790_000_000_000L
    private val up = 5_000_000L // elapsedRealtime "now"

    private fun decide(screenOn: Boolean, offFor: Long? = null, liveUntil: Long = 0) = PowerMode.decide(
        screenOn = screenOn,
        screenOffAtElapsed = offFor?.let { up - it },
        nowElapsed = up,
        liveUntilEpoch = liveUntil,
        nowEpoch = now,
    )

    @Test
    fun `the screen on is ACTIVE`() = assertEquals(PowerMode.ACTIVE, decide(screenOn = true))

    @Test
    fun `a glance at the clock is not a mode change`() =
        assertEquals(PowerMode.ACTIVE, decide(screenOn = false, offFor = 59_000))

    @Test
    fun `a minute after the screen went off it is PASSIVE`() =
        assertEquals(PowerMode.PASSIVE, decide(screenOn = false, offFor = 60_000))

    @Test
    fun `a screen off since nobody knows when is PASSIVE`() =
        assertEquals(PowerMode.PASSIVE, decide(screenOn = false, offFor = null))

    @Test
    fun `Live keeps it ACTIVE with the screen off`() =
        assertEquals(PowerMode.ACTIVE, decide(screenOn = false, offFor = 3_600_000, liveUntil = now + 1))

    @Test
    fun `Live that has ended does not`() =
        assertEquals(PowerMode.PASSIVE, decide(screenOn = false, offFor = 3_600_000, liveUntil = now))

    @Test
    fun `the next change without an event is the end of the grace or of Live, whichever is first`() {
        val grace = PowerMode.nextChangeAtEpoch(false, up - 10_000, up, now + 600_000, now)
        assertEquals(now + 50_000, grace)
        val live = PowerMode.nextChangeAtEpoch(false, up - 3_600_000, up, now + 600_000, now)
        assertEquals(now + 600_000, live)
        assertNull("the screen on changes only by an event", PowerMode.nextChangeAtEpoch(true, null, up, 0, now))
        assertNull("nothing pending", PowerMode.nextChangeAtEpoch(false, up - 3_600_000, up, 0, now))
    }
}
