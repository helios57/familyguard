package io.github.helios57.familyguard.sync

import org.junit.Assert.assertEquals
import org.junit.Test

/** FR-27.2: during Live one position is reported about every 10 s, however many providers answer. */
class LiveThrottleTest {
    @Test
    fun `the first fix goes, and then one per interval`() {
        val throttle = LiveThrottle(minIntervalMillis = 8_000)
        val sent = listOf(0L, 1_000, 5_000, 8_000, 9_000, 16_500, 16_600).filter(throttle::take)
        assertEquals(listOf(0L, 8_000, 16_500), sent)
    }

    @Test
    fun `a new session starts afresh`() {
        val throttle = LiveThrottle(minIntervalMillis = 8_000)
        throttle.take(100_000)
        throttle.reset()
        assertEquals(true, throttle.take(100_500))
    }
}
