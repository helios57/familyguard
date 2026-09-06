package io.github.helios57.familyguard.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The cadence of FR-15.6, and the two ways the version it replaced could not have worked.
 *
 * The first is the clock: the old cadence was `delay(2 min)` in a coroutine, and a coroutine delay
 * does not advance while the phone is suspended, so on the pilot phone it had not elapsed after 38
 * minutes of wall clock. Every test here moves `now` and nothing else — which is what a sleeping
 * phone does to `System.currentTimeMillis()` — and asserts that the answer changes.
 *
 * The second is memory: a countdown held in a coroutine restarts whenever the thing holding it
 * restarts. So the instant is written down, and `arm` is asserted to keep one that has already
 * passed rather than to start again from it.
 */
class UpdateScheduleTest {

    private class Clock(var millis: Long) : () -> Long {
        override fun invoke(): Long = millis
    }

    private fun schedule(clock: Clock, store: UpdateScheduleStore = InMemoryUpdateScheduleStore()) =
        UpdateSchedule(store, clock)

    @Test
    fun `nothing is owed before the first arm`() {
        val clock = Clock(1_000_000)
        val schedule = schedule(clock)

        assertFalse(
            "a check was owed before anything had a credential to make it with",
            schedule.isDue(),
        )
    }

    @Test
    fun `the first check is booked ahead and is not owed yet`() {
        val clock = Clock(1_000_000)
        val schedule = schedule(clock)

        val at = schedule.arm()

        assertEquals(
            "the first check is not FIRST_CHECK_MILLIS after arming",
            clock.millis + UpdateSchedule.FIRST_CHECK_MILLIS,
            at,
        )
        assertFalse("the first check is owed the instant the connection comes up", schedule.isDue())
    }

    /**
     * The property the old implementation did not have.
     *
     * Nothing here runs, sleeps or ticks: the only thing that changes is the wall clock, exactly as
     * it does for a phone that spends the interval suspended in a pocket.
     */
    @Test
    fun `time passing while the phone sleeps is what makes a check fall due`() {
        val clock = Clock(1_000_000)
        val schedule = schedule(clock)
        schedule.arm()

        clock.millis += UpdateSchedule.FIRST_CHECK_MILLIS - 1
        assertFalse("due a millisecond early", schedule.isDue())

        clock.millis += 1
        assertTrue(
            "the wall clock passed the booked instant and no check was owed — which is the defect " +
                "this class exists to end",
            schedule.isDue(),
        )
    }

    @Test
    fun `an instant that has already passed is kept, not restarted`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()
        schedule(clock, store).arm()
        // Off for an hour: the check fell due while nothing was running to take it.
        clock.millis += 60 * 60 * 1000L

        val at = schedule(clock, store).arm()

        assertTrue(
            "arming pushed a check that was already owed back into the future; a phone that " +
                "reconnects more often than the first wait then never checks at all",
            at <= clock.millis,
        )
        assertTrue("the overdue check is not owed after arming", schedule(clock, store).isDue())
    }

    @Test
    fun `a restart does not restart the wait`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()
        val first = schedule(clock, store).arm()

        clock.millis += UpdateSchedule.FIRST_CHECK_MILLIS / 2
        val second = schedule(clock, store).arm()

        assertEquals(
            "a second arm booked a new instant, so a service restarted every minute would owe a " +
                "check every two minutes and never reach one",
            first,
            second,
        )
    }

    /**
     * A clock that moved is the one case a stored instant cannot be trusted.
     *
     * `System.currentTimeMillis()` is settable — by the network, by a child in Settings — and an
     * instant further out than anything this class books cannot have been written by this class
     * against the clock the phone has now.
     */
    @Test
    fun `an unreachable instant is replaced rather than waited for`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()
        schedule(clock, store).arm()
        // The clock jumps back a week; every instant written before it is now a week away.
        clock.millis -= 7 * 24 * 60 * 60 * 1000L

        val at = schedule(clock, store).arm()

        assertEquals(
            "the phone is waiting for an instant its clock will not reach for a week",
            clock.millis + UpdateSchedule.FIRST_CHECK_MILLIS,
            at,
        )
    }

    /**
     * The calibration for the test above: the same code path, with the stored instant exactly at
     * the furthest this class books, must *keep* it. Without this, "replace anything ahead of now"
     * would pass that test and would also throw away every legitimate back-off.
     */
    @Test
    fun `the longest booking this class makes is not mistaken for a moved clock`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()
        val backedOff = schedule(clock, store).refused()

        val at = schedule(clock, store).arm()

        assertEquals(
            "a six-hour back-off was read as a clock that had moved, so a refusal that repeats " +
                "would be retried every two minutes",
            backedOff,
            at,
        )
    }

    @Test
    fun `a check that ran books the ordinary interval and a refusal books the long one`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()

        assertEquals(
            "the interval after an ordinary check is not INTERVAL_MILLIS",
            clock.millis + UpdateSchedule.INTERVAL_MILLIS,
            schedule(clock, store).checked(),
        )
        assertEquals("the ordinary interval was not written down", store.dueAt(), clock.millis + UpdateSchedule.INTERVAL_MILLIS)

        assertEquals(
            "the wait after a refusal is not RETRY_MILLIS",
            clock.millis + UpdateSchedule.RETRY_MILLIS,
            schedule(clock, store).refused(),
        )
        assertEquals("the back-off was not written down", store.dueAt(), clock.millis + UpdateSchedule.RETRY_MILLIS)

        assertTrue(
            "a refusal does not back off further than an ordinary check; the whole point of the " +
                "two numbers is that a repeating refusal is not retried every quarter of an hour",
            UpdateSchedule.RETRY_MILLIS > UpdateSchedule.INTERVAL_MILLIS,
        )
    }

    @Test
    fun `booking a check clears one that was owed`() {
        val clock = Clock(1_000_000)
        val store = InMemoryUpdateScheduleStore()
        schedule(clock, store).arm()
        clock.millis += UpdateSchedule.FIRST_CHECK_MILLIS
        assertTrue("the setup did not produce an owed check", schedule(clock, store).isDue())

        schedule(clock, store).checked()

        assertFalse(
            "the check stayed owed after it ran, so every sync would run another one",
            schedule(clock, store).isDue(),
        )
    }
}
