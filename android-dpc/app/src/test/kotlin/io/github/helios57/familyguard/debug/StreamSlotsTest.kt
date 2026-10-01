package io.github.helios57.familyguard.debug

import java.util.concurrent.CountDownLatch
import java.util.concurrent.CyclicBarrier
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * The cap on open debug streams (FR-19) is a reservation, not a look. It used to be read when a
 * command arrived and counted only once the stream was spliced, seconds of port lookups later — so
 * two commands handled at once both read "3 open" and both opened, and the phone carried five.
 */
class StreamSlotsTest {

    @Test
    fun `a slot is taken at once, so concurrent takes never pass the cap`() {
        // Many rounds, each releasing sixteen takers through one barrier: a single round lets the
        // threads start staggered and a check-then-count version passes it (measured).
        val pool = Executors.newFixedThreadPool(THREADS)
        try {
            repeat(ROUNDS) { round ->
                val slots = StreamSlots(max = 4)
                val barrier = CyclicBarrier(THREADS)
                val taken = AtomicInteger()
                val done = CountDownLatch(THREADS)
                repeat(THREADS) {
                    pool.execute {
                        barrier.await()
                        if (slots.tryTake() != null) taken.incrementAndGet()
                        done.countDown()
                    }
                }
                done.await(10, TimeUnit.SECONDS)
                assertEquals("round $round: $THREADS commands at once against a cap of 4", 4, taken.get())
                assertEquals(4, slots.inUse)
            }
        } finally {
            pool.shutdownNow()
        }
    }

    @Test
    fun `a released slot can be taken again, and the count says how many are open`() {
        val slots = StreamSlots(max = 2)
        assertEquals(1, slots.tryTake())
        assertEquals(2, slots.tryTake())
        assertNull("a third stream past a cap of 2", slots.tryTake())
        assertEquals(1, slots.release())
        assertEquals(2, slots.tryTake())
    }

    private companion object {
        const val THREADS = 16
        const val ROUNDS = 300
    }
}
