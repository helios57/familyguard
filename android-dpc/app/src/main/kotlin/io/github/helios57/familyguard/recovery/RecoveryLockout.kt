package io.github.helios57.familyguard.recovery

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * What the lockout remembers between attempts, and across reboots.
 *
 * Persisted, because a lockout held only in memory is defeated by turning the phone off and on
 * again — which is the first thing anyone does to a phone that will not let them in, and the one
 * thing a child locked out of their apps has every reason to try.
 */
@Serializable
data class LockoutState(
    @SerialName("consecutive_failures") val consecutiveFailures: Int = 0,
    @SerialName("locked_until") val lockedUntilEpochMillis: Long = 0,
)

interface LockoutStore {
    fun load(): LockoutState
    fun save(state: LockoutState)
}

/** A [LockoutStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryLockoutStore(initial: LockoutState = LockoutState()) : LockoutStore {
    private var state = initial
    override fun load(): LockoutState = state
    override fun save(state: LockoutState) {
        this.state = state
    }
}

/** Whether an attempt may be made right now, and if not, for how much longer. */
sealed interface LockoutStatus {
    data object Open : LockoutStatus
    data class Closed(val remainingMillis: Long) : LockoutStatus
}

/**
 * Rate-limits recovery attempts with an escalating, persisted, bounded delay (FR-12.4, NFR-6).
 *
 * The code is ~100 bits from a 30-character alphabet, so this is not what stops a brute force —
 * nothing could guess it in a hundred lifetimes at one attempt per second. What it stops is the
 * shoulder-surfing case: a child who saw four of the five groups and is working through the fifth,
 * which is 30 guesses and takes under a minute without a delay.
 *
 * Two properties matter more than the exact numbers:
 *
 * **The first two mistakes cost nothing.** A parent reading a code off a screen mistypes; making
 * them wait for that trains them to distrust the escape hatch, and an escape hatch nobody reaches
 * for is not one.
 *
 * **It is never permanent.** [MAX_LOCKOUT_MILLIS] caps the wait at an hour no matter how many
 * failures accumulate. NFR-6 says a lockdown that depends on something working must fail open if
 * that thing is not working, and a recovery path that locks itself shut forever is precisely the
 * brick this whole feature exists to prevent. A child who guesses two hundred times has cost the
 * parent an hour, not the phone.
 *
 * The clock is the other way this could become permanent, and it is handled rather than assumed
 * away: a deadline further out than the cap cannot have been written by this code against this
 * clock, so it is the clock that moved and the deadline is dropped. Without that, a phone whose
 * time zone or NTP sync jumped it backwards a year would refuse every attempt for a year.
 */
class RecoveryLockout(
    private val store: LockoutStore,
    private val now: () -> Long = System::currentTimeMillis,
) {

    fun status(): LockoutStatus {
        val state = reconciled()
        val remaining = state.lockedUntilEpochMillis - now()
        return if (remaining > 0) LockoutStatus.Closed(remaining) else LockoutStatus.Open
    }

    /** @return the status that now applies — the caller shows the wait it just earned. */
    fun recordFailure(): LockoutStatus {
        val failures = reconciled().consecutiveFailures + 1
        val wait = waitAfter(failures)
        store.save(LockoutState(failures, if (wait > 0) now() + wait else 0))
        return if (wait > 0) LockoutStatus.Closed(wait) else LockoutStatus.Open
    }

    /** A code that verified clears the history. The next mistake starts from zero again. */
    fun recordSuccess() = store.save(LockoutState())

    /** How many consecutive failures are on record. Shown to the parent, and reported. */
    fun consecutiveFailures(): Int = reconciled().consecutiveFailures

    /**
     * The stored state with an impossible deadline removed.
     *
     * Reconciling on read rather than repairing on write, because the clock can move at any moment
     * and the last write may have happened under the old one.
     */
    private fun reconciled(): LockoutState {
        val state = store.load()
        val remaining = state.lockedUntilEpochMillis - now()
        if (remaining <= MAX_LOCKOUT_MILLIS) return state
        // The deadline outruns the longest lockout this class can impose, so it was written against
        // a different clock. The failure count is kept — that is a fact about attempts, not about
        // time — and only the deadline is dropped.
        val repaired = state.copy(lockedUntilEpochMillis = 0)
        store.save(repaired)
        return repaired
    }

    private fun waitAfter(failures: Int): Long = when {
        failures <= FREE_ATTEMPTS -> 0
        else -> ESCALATION.getOrElse(failures - FREE_ATTEMPTS - 1) { MAX_LOCKOUT_MILLIS }
    }

    companion object {
        /** Mistypes that cost nothing. The third failure is the first that waits. */
        const val FREE_ATTEMPTS = 2

        /** No lockout is ever longer than this, however many failures accumulate. */
        const val MAX_LOCKOUT_MILLIS = 60L * 60 * 1000

        /**
         * The wait after the third, fourth, fifth and sixth failure. Anything beyond holds at the
         * last entry, which is [MAX_LOCKOUT_MILLIS] — stated here rather than left to the fallback
         * so the escalation reads as a finite list that ends somewhere.
         */
        val ESCALATION: List<Long> = listOf(
            30L * 1000,
            2L * 60 * 1000,
            10L * 60 * 1000,
            MAX_LOCKOUT_MILLIS,
        )
    }
}
