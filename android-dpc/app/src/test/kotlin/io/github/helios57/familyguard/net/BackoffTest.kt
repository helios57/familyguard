package io.github.helios57.familyguard.net

import kotlin.random.Random
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class BackoffTest {

    /** Draws the top of the range every time, so the ceiling itself is what gets asserted. */
    private class Ceiling : Random() {
        override fun nextBits(bitCount: Int): Int = throw UnsupportedOperationException()
        override fun nextLong(until: Long): Long = until - 1
    }

    private fun ceilings(count: Int, base: Long = 1_000, max: Long = 300_000): List<Long> {
        val backoff = Backoff(baseMillis = base, maxMillis = max, random = Ceiling())
        return (1..count).map { backoff.nextDelayMillis() }
    }

    @Test
    fun `the ceiling doubles per failure`() {
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 16_000L), ceilings(5))
    }

    @Test
    fun `the ceiling is clamped at the maximum and stays there`() {
        val tail = ceilings(20).takeLast(5)
        assertEquals(listOf(300_000L, 300_000L, 300_000L, 300_000L, 300_000L), tail)
    }

    @Test
    fun `a phone offline for a very long time does not wrap round to the base delay`() {
        // 1L shl 64 is 1L on the JVM, not an overflow. Without the exponent cap the 64th failure
        // would produce the shortest delay in the schedule, and a device that had been unreachable
        // for a day would come back hammering.
        val backoff = Backoff(baseMillis = 1_000, maxMillis = 300_000, random = Ceiling())
        repeat(200) { backoff.nextDelayMillis() }
        assertEquals(300_000L, backoff.nextDelayMillis())
    }

    @Test
    fun `every delay is drawn from the full range below the ceiling`() {
        // Full jitter, not a band: the point is that two phones reconnecting after the same backend
        // restart do not stay in step. A band around the exponential keeps them correlated.
        val backoff = Backoff(baseMillis = 1_000, maxMillis = 300_000, random = Random(7))
        val drawn = (1..500).map { backoff.nextDelayMillis() }
        assertTrue("saw $drawn", drawn.all { it in 0..300_000 })
        assertTrue("no small delays drawn: ${drawn.min()}", drawn.min() < 30_000)
        assertTrue("no large delays drawn: ${drawn.max()}", drawn.max() > 200_000)
    }

    @Test
    fun `reset returns the schedule to the base delay`() {
        val backoff = Backoff(baseMillis = 1_000, maxMillis = 300_000, random = Ceiling())
        repeat(6) { backoff.nextDelayMillis() }
        assertEquals(6, backoff.failures)
        backoff.reset()
        assertEquals(0, backoff.failures)
        assertEquals(1_000L, backoff.nextDelayMillis())
    }

    @Test
    fun `a base equal to the maximum is a constant schedule`() {
        assertEquals(listOf(5_000L, 5_000L, 5_000L), ceilings(3, base = 5_000, max = 5_000))
    }
}
