package io.github.helios57.familyguard.enforce

import java.time.OffsetDateTime

/**
 * Turns [DesiredState.nextChangeAt] into a wake-up.
 *
 * Without this the engine's own promise is not kept. [EnforcementEngine] computes the next instant
 * at which the answer changes — the bedtime edge, or the midnight the quota resets at — and the
 * device only ever re-evaluated while the screen was on, five minutes at a time. A phone put down at
 * 20:30 with bedtime at 21:00 therefore enforced nothing at 21:00; the child picked it up at 21:15
 * to a phone that had not started bedtime and would not for up to another five minutes. Nothing goes
 * red: the console shows the bedtime configured, the state carries `next_change_at`, and the device
 * agrees with all of it — it is simply asleep.
 *
 * The rule lives here, away from `AlarmManager`, because every interesting case is a decision rather
 * than a platform call: an empty instant must cancel, an unreadable one must *not*, and one already
 * in the past must not turn into a wake loop.
 */
class EnforcementAlarm(
    private val platform: AlarmPlatform,
    private val now: () -> Long,
) {

    /**
     * Books the next wake-up for [state], replacing whatever was booked before.
     *
     * Deliberately unconditional: re-booking the same instant costs one binder call, while skipping
     * it needs this class to know whether the alarm it booked has already fired. It cannot know that,
     * and the failure mode of guessing wrong is a device that never wakes again.
     */
    fun schedule(state: DesiredState): AlarmDecision {
        val value = state.nextChangeAt
        if (value.isEmpty()) {
            // Nothing about this policy changes on its own: no bedtime, no daily limit. A wake-up
            // left booked from an earlier policy would fire for the rest of the device's life.
            platform.cancel()
            return AlarmDecision.Cancelled
        }

        val parsed = parse(value)
        if (parsed == null) {
            // The alarm already booked is left alone on purpose. Cancelling would trade "the next
            // edge is at an unknown time" for "there is no next edge", and the second is worse: the
            // device stops waking entirely, which looks identical to a policy with nothing scheduled.
            return AlarmDecision.Unreadable(value)
        }

        // An instant already past means this state was computed against a clock that has since moved
        // — a slow sync, or a device clock corrected between the two. Enforcing it is right; doing so
        // with no floor is not, because a state that keeps producing a past instant would wake the
        // phone as fast as the alarm can be re-booked.
        val atMillis = if (parsed <= now()) now() + MINIMUM_DELAY_MILLIS else parsed
        return when (platform.schedule(atMillis)) {
            AlarmBooking.EXACT -> AlarmDecision.Scheduled(atMillis, exact = true)
            AlarmBooking.INEXACT -> AlarmDecision.Scheduled(atMillis, exact = false)
            AlarmBooking.REFUSED -> AlarmDecision.Refused(atMillis, platform.unavailableReason())
        }
    }

    private fun parse(value: String): Long? =
        runCatching { OffsetDateTime.parse(value).toInstant().toEpochMilli() }.getOrNull()

    private companion object {
        /** The shortest a past-due edge may be deferred by, which is what bounds the wake rate. */
        const val MINIMUM_DELAY_MILLIS = 10_000L
    }
}

/** What one booking attempt did. Reported rather than returned as a boolean so the log can say. */
enum class AlarmBooking {
    /** Booked to fire at the requested instant, through doze. */
    EXACT,

    /**
     * Booked, but the platform may delay it. Bedtime then starts late rather than not at all, which
     * is worth having — but it is a different promise, so it is a different value.
     */
    INEXACT,

    /** Nothing was booked. [AlarmPlatform.unavailableReason] says why. */
    REFUSED,
}

/** Where a wake-up is actually booked. Separated so the rule above runs on the JVM. */
interface AlarmPlatform {
    fun schedule(atMillis: Long): AlarmBooking

    fun cancel()

    /** Why the last [schedule] was not [AlarmBooking.EXACT]. */
    fun unavailableReason(): String
}

/** The outcome of [EnforcementAlarm.schedule], as the service logs it. */
sealed interface AlarmDecision {

    data class Scheduled(val atMillis: Long, val exact: Boolean) : AlarmDecision {
        override fun toString(): String =
            "next enforcement wake-up at $atMillis (${if (exact) "exact" else "INEXACT"})"
    }

    /** The policy has no next change, so any wake-up left over from an earlier one was cancelled. */
    data object Cancelled : AlarmDecision {
        override fun toString(): String = "no next change; any enforcement wake-up was cancelled"
    }

    /** The instant could not be read. Whatever was booked before is still booked. */
    data class Unreadable(val value: String) : AlarmDecision {
        override fun toString(): String =
            "next change NOT SCHEDULED: \"$value\" is not an instant; the previous wake-up stands"
    }

    data class Refused(val atMillis: Long, val reason: String) : AlarmDecision {
        override fun toString(): String =
            "next change NOT SCHEDULED for $atMillis — $reason"
    }
}
