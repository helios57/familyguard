package io.github.helios57.familyguard.alarm

import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** FR-23: what the phone books — the next ring, or a snooze if that comes first. */
class AlarmBookingTest {

    private val s = AlarmSchedule(timezone = "UTC", weekdays = List(7) { "06:30" })
    private val now = Instant.parse("2026-09-28T05:00:00Z")

    @Test
    fun `with nothing pending the next scheduled ring is booked`() {
        assertEquals(Instant.parse("2026-09-28T06:30:00Z"), AlarmBooking.next(s, now, AlarmState()))
    }

    @Test
    fun `a snooze earlier than the next ring is what is booked`() {
        val snooze = Instant.parse("2026-09-28T06:35:00Z")
        val after = Instant.parse("2026-09-28T06:31:00Z")
        assertEquals(snooze, AlarmBooking.next(s, after, AlarmState(snoozeUntil = snooze, lastRang = Instant.parse("2026-09-28T06:30:00Z"))))
    }

    @Test
    fun `a ring already rung is not booked again, even if the clock reads a moment before it`() {
        val rang = Instant.parse("2026-09-28T06:30:00Z")
        assertEquals(Instant.parse("2026-09-29T06:30:00Z"),
            AlarmBooking.next(s, Instant.parse("2026-09-28T06:29:59Z"), AlarmState(lastRang = rang)))
    }

    @Test
    fun `a snooze in the past is dropped`() {
        val stale = Instant.parse("2026-09-28T04:00:00Z")
        assertEquals(Instant.parse("2026-09-28T06:30:00Z"), AlarmBooking.next(s, now, AlarmState(snoozeUntil = stale)))
    }

    @Test
    fun `no schedule and no snooze books nothing`() {
        assertNull(AlarmBooking.next(AlarmSchedule(), now, AlarmState()))
    }

    @Test
    fun `the alarm volume is raised to half the stream while it rings, and never lowered`() {
        assertEquals(7, AlarmVolume.ringLevel(current = 0, max = 15))
        assertEquals(7, AlarmVolume.ringLevel(current = 3, max = 15))
        assertNull(AlarmVolume.ringLevel(current = 7, max = 15))
        assertNull(AlarmVolume.ringLevel(current = 15, max = 15))
        // Unread: raised, and — since there is nothing to restore — never "restored" to a guess.
        assertEquals(7, AlarmVolume.ringLevel(current = null, max = 15))
    }
}
