package io.github.helios57.familyguard.enforce

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.OffsetDateTime

/**
 * What gets booked, what gets cancelled, and the two cases where doing the obvious thing is wrong.
 *
 * The failure this class exists to prevent is the quietest one in the product: a phone that agrees
 * with the console about everything and is simply asleep at the moment the bedtime starts. Nothing
 * logs an error, the state carries the right `next_change_at`, and the child picks up an
 * unrestricted phone.
 */
class EnforcementAlarmTest {

    private val platform = FakePlatform()
    private var now = at("2026-08-17T20:30:00+02:00")
    private val alarm = EnforcementAlarm(platform) { now }

    @Test
    fun `the next change is booked as an exact wake-up at that instant`() {
        val decision = alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        assertEquals(AlarmDecision.Scheduled(at("2026-08-17T21:00:00+02:00"), exact = true), decision)
        assertEquals(listOf(at("2026-08-17T21:00:00+02:00")), platform.booked)
        assertEquals("booking must not cancel first", 0, platform.cancels)
    }

    /** The offset is part of the instant. Reading it as local time would move bedtime by two hours. */
    @Test
    fun `the same instant written in two zones books the same wake-up`() {
        alarm.schedule(state("2026-08-17T21:00:00+02:00"))
        alarm.schedule(state("2026-08-17T19:00:00Z"))

        assertEquals(2, platform.booked.size)
        assertEquals(platform.booked[0], platform.booked[1])
    }

    /**
     * A policy with no bedtime and no daily limit changes nothing on its own. A wake-up left over
     * from the policy before it would fire for the rest of the device's life, waking the phone every
     * night at a time that no longer means anything.
     */
    @Test
    fun `no next change cancels whatever was booked`() {
        alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        val decision = alarm.schedule(state(""))

        assertEquals(AlarmDecision.Cancelled, decision)
        assertEquals(1, platform.cancels)
        assertEquals("nothing new was booked", 1, platform.booked.size)
    }

    /**
     * The one case where doing nothing is right. An instant that cannot be read means the next edge
     * is at an unknown time; cancelling would turn that into no next edge at all, which is the
     * strictly worse state and is indistinguishable from a policy that never changes.
     */
    @Test
    fun `an unreadable instant leaves the previous wake-up standing`() {
        alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        val decision = alarm.schedule(state("never"))

        assertEquals(AlarmDecision.Unreadable("never"), decision)
        assertEquals("the standing wake-up was cancelled", 0, platform.cancels)
        assertEquals("something was booked for an instant nobody could read", 1, platform.booked.size)
    }

    @Test
    fun `a date with no time is not an instant`() {
        assertTrue(alarm.schedule(state("2026-08-17")) is AlarmDecision.Unreadable)
        assertTrue(platform.booked.isEmpty())
    }

    /**
     * An edge that is already past is enforced, not dropped — the state was computed against a clock
     * that has since moved. The floor is what keeps that from becoming a wake loop: a state that
     * keeps producing a past instant would otherwise re-book as fast as the alarm can fire.
     */
    @Test
    fun `an instant already past is deferred rather than dropped`() {
        val decision = alarm.schedule(state("2026-08-17T20:00:00+02:00"))

        val bookedAt = (decision as AlarmDecision.Scheduled).atMillis
        assertTrue("a past edge must still be enforced", bookedAt > now)
        assertTrue("and not immediately, or it is a wake loop", bookedAt - now >= 10_000)
        assertEquals(listOf(bookedAt), platform.booked)
    }

    @Test
    fun `an instant exactly now is deferred too`() {
        val decision = alarm.schedule(state("2026-08-17T20:30:00+02:00"))

        assertTrue((decision as AlarmDecision.Scheduled).atMillis > now)
    }

    /**
     * Re-booking is unconditional on purpose. Skipping an unchanged instant needs this class to know
     * whether the alarm it booked has already fired, which it cannot; guessing wrong once produces a
     * device that never wakes again.
     */
    @Test
    fun `the same instant is booked again rather than skipped`() {
        alarm.schedule(state("2026-08-17T21:00:00+02:00"))
        alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        assertEquals(2, platform.booked.size)
    }

    // ---- degraded, which is not refused ---------------------------------------------------------

    /**
     * `SCHEDULE_EXACT_ALARM` is an appop the platform can withhold. A wake-up it may delay still
     * starts the bedtime, a few minutes late — worth having, and a different promise, so it is a
     * different answer.
     */
    @Test
    fun `a platform that can only book inexactly says so`() {
        platform.booking = AlarmBooking.INEXACT

        val decision = alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        assertEquals(
            AlarmDecision.Scheduled(at("2026-08-17T21:00:00+02:00"), exact = false),
            decision,
        )
        assertNotEquals(
            "an inexact booking must not compare equal to an exact one",
            AlarmDecision.Scheduled(at("2026-08-17T21:00:00+02:00"), exact = true),
            decision,
        )
    }

    /** The log line is the only place a degraded wake-up is visible, so it has to read as one. */
    @Test
    fun `the inexact case is loud in the line the service logs`() {
        platform.booking = AlarmBooking.INEXACT

        val line = alarm.schedule(state("2026-08-17T21:00:00+02:00")).toString()

        assertTrue(line, line.contains("INEXACT"))
    }

    @Test
    fun `a refused booking carries the platform's reason`() {
        platform.booking = AlarmBooking.REFUSED
        platform.reason = "this device has no AlarmManager"

        val decision = alarm.schedule(state("2026-08-17T21:00:00+02:00"))

        assertEquals(
            AlarmDecision.Refused(at("2026-08-17T21:00:00+02:00"), "this device has no AlarmManager"),
            decision,
        )
        assertTrue(decision.toString().contains("NOT SCHEDULED"))
    }

    private fun state(nextChangeAt: String) = DesiredState(nextChangeAt = nextChangeAt)

    private fun at(text: String): Long = OffsetDateTime.parse(text).toInstant().toEpochMilli()

    private class FakePlatform : AlarmPlatform {
        val booked = mutableListOf<Long>()
        var cancels = 0
        var booking = AlarmBooking.EXACT
        var reason = "not asked yet"

        override fun schedule(atMillis: Long): AlarmBooking {
            if (booking != AlarmBooking.REFUSED) booked += atMillis
            return booking
        }

        override fun cancel() {
            cancels++
        }

        override fun unavailableReason(): String = reason
    }
}
