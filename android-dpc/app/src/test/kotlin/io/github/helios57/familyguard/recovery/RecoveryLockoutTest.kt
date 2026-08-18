package io.github.helios57.familyguard.recovery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The rate limit on recovery attempts (FR-12.4, NFR-6).
 *
 * Written against the two properties [RecoveryLockout] documents as load-bearing rather than against
 * the numbers, because the numbers are a judgement call and the properties are not:
 *
 * - **the first mistakes cost nothing**, so a parent reading twenty characters off another screen is
 *   not punished for a typo on the one screen that exists for when everything else has failed;
 * - **it is never permanent**, so no sequence of failures — and no clock — can turn the escape hatch
 *   into the brick it exists to prevent.
 *
 * The clock is injected. A test that slept would take an hour to reach the cap, and one that
 * asserted on `System.currentTimeMillis` would be measuring the machine it runs on.
 */
class RecoveryLockoutTest {

    private var clock = START
    private val store = InMemoryLockoutStore()
    private val lockout = RecoveryLockout(store) { clock }

    private fun closedFor(status: LockoutStatus): Long =
        (status as? LockoutStatus.Closed)?.remainingMillis
            ?: throw AssertionError("expected a lockout, got $status")

    // ---- the mistakes that cost nothing ------------------------------------------------------

    @Test
    fun `a device that has never failed may attempt immediately`() {
        assertEquals(LockoutStatus.Open, lockout.status())
        assertEquals(0, lockout.consecutiveFailures())
    }

    /**
     * FREE_ATTEMPTS is 2, so the first two failures return Open and leave the screen usable.
     *
     * Asserted through [RecoveryLockout.status] as well as through the return value: an
     * implementation that returned Open while writing a deadline would pass on the return alone, and
     * the parent would find the next attempt refused for a reason nothing told them about.
     */
    @Test
    fun `the first two mistakes cost nothing`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS) { attempt ->
            assertEquals("failure ${attempt + 1} imposed a wait", LockoutStatus.Open, lockout.recordFailure())
            assertEquals(LockoutStatus.Open, lockout.status())
        }
        assertEquals(RecoveryLockout.FREE_ATTEMPTS, lockout.consecutiveFailures())
    }

    @Test
    fun `the third mistake is the first that waits`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS) { lockout.recordFailure() }

        val wait = closedFor(lockout.recordFailure())

        assertEquals(RecoveryLockout.ESCALATION.first(), wait)
        assertEquals(wait, closedFor(lockout.status()))
    }

    /**
     * The policy numbers, stated once as literals.
     *
     * Every other test in this file reaches the constants through [RecoveryLockout] as symbols,
     * which is what keeps them readable — and what makes them survive a change to the numbers. That
     * second half is a hole, and it was measured rather than reasoned about: raising
     * `FREE_ATTEMPTS` from 2 to 3 left this entire suite green, the test named `the third mistake is
     * the first that waits` included, which then cheerfully asserted that the *fourth* was. A suite
     * parameterised by the constant it is checking cannot see that constant move.
     *
     * These numbers belong to FR-12.4 and NFR-6, not to this class, so one test writes them out in
     * the form a reader can hold against the requirement. Changing the policy has to change this
     * line — that is the whole job.
     */
    @Test
    fun `the escalation is the one FR-12_4 specifies, in literal milliseconds`() {
        assertEquals("FR-12.4: two mistypes cost nothing", 2, RecoveryLockout.FREE_ATTEMPTS)
        assertEquals(
            "FR-12.4: 30 s, then 2 min, then 10 min, then an hour",
            listOf(30_000L, 120_000L, 600_000L, 3_600_000L),
            RecoveryLockout.ESCALATION,
        )
        assertEquals(
            "NFR-6: no lockout is ever longer than an hour",
            3_600_000L,
            RecoveryLockout.MAX_LOCKOUT_MILLIS,
        )
    }

    // ---- escalation, and its ceiling ---------------------------------------------------------

    /**
     * Each further failure waits longer, up to the last entry of the escalation and no further.
     *
     * The loop runs well past the end of the list on purpose: the fallback in `waitAfter` is what
     * turns "the list ran out" into the cap rather than into an index out of bounds or a zero, and a
     * test that stopped at the last entry would never reach it.
     */
    @Test
    fun `the wait escalates through the list and then holds at the cap`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS) { lockout.recordFailure() }

        for ((index, expected) in RecoveryLockout.ESCALATION.withIndex()) {
            clock += expected + 1 // serve the wait just earned, so the next attempt is allowed
            assertEquals(
                "the wait after failure ${RecoveryLockout.FREE_ATTEMPTS + index + 1}",
                expected,
                closedFor(lockout.recordFailure()),
            )
        }

        repeat(20) {
            clock += RecoveryLockout.MAX_LOCKOUT_MILLIS + 1
            assertEquals(
                "a wait past the end of the escalation exceeded the cap",
                RecoveryLockout.MAX_LOCKOUT_MILLIS,
                closedFor(lockout.recordFailure()),
            )
        }
    }

    /**
     * NFR-6, stated as the thing that must never happen: a child guessing all afternoon costs the
     * parent an hour of waiting, not the phone.
     */
    @Test
    fun `no number of failures can lock the device for longer than the cap`() {
        repeat(200) { lockout.recordFailure() }

        val remaining = closedFor(lockout.status())
        assertTrue(
            "200 failures produced a $remaining ms lockout, past the ${RecoveryLockout.MAX_LOCKOUT_MILLIS} ms cap",
            remaining <= RecoveryLockout.MAX_LOCKOUT_MILLIS,
        )
        assertEquals(200, lockout.consecutiveFailures())
    }

    @Test
    fun `a wait that has been served reopens the device without any success being recorded`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS + 1) { lockout.recordFailure() }
        val wait = closedFor(lockout.status())

        clock += wait - 1
        assertEquals("the lockout ended a millisecond early", LockoutStatus.Closed(1), lockout.status())

        clock += 1
        assertEquals(LockoutStatus.Open, lockout.status())
        // The count survives the wait. Serving 30 seconds does not buy back the free attempts, or a
        // guesser would simply wait 30 seconds between every guess for the rest of time.
        assertEquals(RecoveryLockout.FREE_ATTEMPTS + 1, lockout.consecutiveFailures())
    }

    // ---- persistence -------------------------------------------------------------------------

    /**
     * The first thing anyone does to a phone that will not let them in.
     *
     * A new [RecoveryLockout] over the same store is what a reboot produces — the object is gone, the
     * file is not. A lockout held in a field would be Open here, and the whole rate limit would be
     * one power cycle away from useless.
     */
    @Test
    fun `a reboot does not clear the lockout`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS + 2) { lockout.recordFailure() }
        val remainingBefore = closedFor(lockout.status())

        val afterReboot = RecoveryLockout(store) { clock }

        assertEquals(remainingBefore, closedFor(afterReboot.status()))
        assertEquals(RecoveryLockout.FREE_ATTEMPTS + 2, afterReboot.consecutiveFailures())
    }

    @Test
    fun `a code that verified clears the history`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS + 3) { lockout.recordFailure() }

        lockout.recordSuccess()

        assertEquals(LockoutStatus.Open, lockout.status())
        assertEquals(0, lockout.consecutiveFailures())
        // And the next mistake is free again, rather than resuming where the escalation left off.
        assertEquals(LockoutStatus.Open, lockout.recordFailure())
    }

    // ---- the clock -----------------------------------------------------------------------------

    /**
     * A phone whose clock jumps backwards — a timezone change, an NTP correction, a user setting the
     * date — must not be locked out until the clock catches up.
     *
     * The stored deadline here is one millisecond past what this class could ever impose, which is
     * the only evidence available that it was written against a different clock: nothing records
     * which clock a number came from, so "further out than possible" is the whole test.
     */
    @Test
    fun `a deadline further out than the cap is dropped as a clock that moved`() {
        store.save(LockoutState(consecutiveFailures = 5, lockedUntilEpochMillis = clock + RecoveryLockout.MAX_LOCKOUT_MILLIS + 1))

        assertEquals(LockoutStatus.Open, lockout.status())
        // The count is a fact about attempts, not about time, so it survives the repair. Clearing it
        // would hand a free escalation to anyone who can change the phone's clock.
        assertEquals(5, lockout.consecutiveFailures())
        assertEquals(
            "the repair was not persisted, so it happens again on every read",
            0,
            store.load().lockedUntilEpochMillis,
        )
    }

    /**
     * The boundary, from the other side: a deadline exactly at the cap is legitimate — it is what the
     * last entry of the escalation writes — and must be served, not repaired away.
     *
     * Without this, a `<` where the code has `<=` would silently make the longest lockout the one
     * lockout that does not apply.
     */
    @Test
    fun `a deadline exactly at the cap is a real lockout and is kept`() {
        store.save(LockoutState(consecutiveFailures = 6, lockedUntilEpochMillis = clock + RecoveryLockout.MAX_LOCKOUT_MILLIS))

        assertEquals(RecoveryLockout.MAX_LOCKOUT_MILLIS, closedFor(lockout.status()))
        assertEquals(
            "a legitimate deadline was repaired away",
            clock + RecoveryLockout.MAX_LOCKOUT_MILLIS,
            store.load().lockedUntilEpochMillis,
        )
    }

    /** A clock that moved forward instead just ends the wait, which needs no repair at all. */
    @Test
    fun `a clock that jumped forward ends the wait rather than being treated as an error`() {
        repeat(RecoveryLockout.FREE_ATTEMPTS + 1) { lockout.recordFailure() }
        clock += 365L * 24 * 60 * 60 * 1000

        assertEquals(LockoutStatus.Open, lockout.status())
        assertEquals(RecoveryLockout.FREE_ATTEMPTS + 1, lockout.consecutiveFailures())
    }

    private companion object {
        /** An arbitrary fixed instant. Nothing here depends on when it is. */
        const val START = 1_755_000_000_000L
    }
}
