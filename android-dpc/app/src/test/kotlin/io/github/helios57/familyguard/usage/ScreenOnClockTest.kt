package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The ceiling on how much screen time a window can possibly have contained.
 *
 * Everything here is arithmetic on one monotonic clock, and every case is a way the real device is
 * untidy: duplicate broadcasts, a poll that lands mid-session, a reboot that resets the clock to
 * zero. What must never happen is that a night with the screen off ends up in the budget, because
 * whatever app was last in the foreground would be credited with all of it.
 */
class ScreenOnClockTest {

    private val minute = 60_000L

    @Test
    fun `a session that is still open is drained up to now`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        assertEquals(10 * minute, clock.drain(10 * minute))
    }

    @Test
    fun `draining mid-session neither double-counts nor loses the rest`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        assertEquals(10 * minute, clock.drain(10 * minute))
        assertEquals(5 * minute, clock.drain(15 * minute))
    }

    @Test
    fun `time with the screen off is not in the budget`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        clock.onScreenOff(5 * minute)

        assertEquals("only the five minutes before screen-off", 5 * minute, clock.drain(8 * 60 * minute))
    }

    /** The whole of FR-3.3 in one case: a window entirely asleep contributes nothing at all. */
    @Test
    fun `a window with the screen off throughout drains zero`() {
        val clock = ScreenOnClock(screenOn = false, startMillis = 0)

        assertEquals(0L, clock.drain(8 * 60 * minute))
    }

    @Test
    fun `the interval banked at screen-off is kept until the next drain`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        clock.onScreenOff(5 * minute)
        clock.onScreenOn(60 * minute)

        assertEquals(5 * minute + 3 * minute, clock.drain(63 * minute))
    }

    @Test
    fun `two sessions in one window are both counted`() {
        val clock = ScreenOnClock(screenOn = false, startMillis = 0)

        clock.onScreenOn(1 * minute)
        clock.onScreenOff(3 * minute)
        clock.onScreenOn(10 * minute)
        clock.onScreenOff(14 * minute)

        assertEquals(6 * minute, clock.drain(20 * minute))
        assertEquals("the banked total was reset by the drain", 0L, clock.drain(30 * minute))
    }

    // ---- untidy input --------------------------------------------------------------------------

    /**
     * `ACTION_SCREEN_ON` arrives more than once in practice, and a second one that reset `since`
     * would silently discard the minutes already elapsed in the open session.
     */
    @Test
    fun `a repeated screen-on broadcast changes nothing`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        assertFalse(clock.onScreenOn(5 * minute))

        assertEquals(10 * minute, clock.drain(10 * minute))
    }

    /** The mirror image: a duplicate screen-off must not bank the same interval twice. */
    @Test
    fun `a repeated screen-off broadcast banks nothing further`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 0)

        assertTrue(clock.onScreenOff(5 * minute))
        assertFalse(clock.onScreenOff(9 * minute))

        assertEquals(5 * minute, clock.drain(20 * minute))
    }

    /**
     * `elapsedRealtime` restarts at zero after a reboot, so a value captured before one is larger
     * than every value after it. Counting the difference as zero costs at most one window; treating
     * it as negative would subtract from a ceiling on a child's screen time.
     */
    @Test
    fun `a backwards timestamp counts as no time, never as negative`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 10 * minute)

        assertEquals(0L, clock.drain(0))
    }

    @Test
    fun `a backwards screen-off banks nothing and still closes the session`() {
        val clock = ScreenOnClock(screenOn = true, startMillis = 10 * minute)

        clock.onScreenOff(0)

        assertFalse(clock.isScreenOn())
        assertEquals(0L, clock.drain(60 * minute))
    }

    @Test
    fun `the reported state follows the broadcasts`() {
        val clock = ScreenOnClock(screenOn = false, startMillis = 0)

        assertFalse(clock.isScreenOn())
        assertTrue(clock.onScreenOn(minute))
        assertTrue(clock.isScreenOn())
        assertTrue(clock.onScreenOff(2 * minute))
        assertFalse(clock.isScreenOn())
    }
}
