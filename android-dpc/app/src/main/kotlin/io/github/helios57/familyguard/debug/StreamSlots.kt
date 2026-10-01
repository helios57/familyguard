package io.github.helios57.familyguard.debug

import java.util.concurrent.atomic.AtomicInteger

/**
 * How many debug streams this phone relays at once, taken as a reservation (FR-19).
 *
 * [tryTake] checks the cap and counts the stream in one step. Reading the count when a command
 * arrived and adding to it only once the stream was spliced left the seconds of port lookups in
 * between, and two commands handled at once each saw room for one more.
 */
internal class StreamSlots(private val max: Int) {
    private val count = AtomicInteger()

    val inUse: Int get() = count.get()

    /** The number open including this one, or null when the cap is reached. */
    fun tryTake(): Int? {
        while (true) {
            val now = count.get()
            if (now >= max) return null
            if (count.compareAndSet(now, now + 1)) return now + 1
        }
    }

    /** Gives a slot back; the number still open. */
    fun release(): Int = count.decrementAndGet()
}
