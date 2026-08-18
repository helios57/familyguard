package io.github.helios57.familyguard.net

import kotlin.random.Random

/**
 * The delay between reconnection attempts: exponential, capped, with full jitter.
 *
 * Two properties matter more than the curve.
 *
 * **It resets on a stream that was established, not on a socket that was accepted.** A server that
 * accepts the connection and closes it immediately — a proxy in front of a restarting backend does
 * exactly this — would otherwise reset the delay on every attempt, and a phone would hammer it at
 * the base interval indefinitely while reporting that it was reconnecting normally. The stream is
 * established when the server's `connected` frame arrives, which is the whole reason that frame
 * exists, and [reset] is called from there and nowhere else.
 *
 * **The jitter is full, not a percentage band.** Every device in a family reconnects when the
 * backend restarts, and a narrow band around a shared exponential keeps them in step for hours. A
 * delay drawn uniformly from `[0, cap]` decorrelates them after the first attempt.
 *
 * [random] is injected so the schedule can be measured rather than watched.
 */
class Backoff(
    private val baseMillis: Long = 1_000,
    private val maxMillis: Long = 300_000,
    private val random: Random = Random.Default,
) {
    init {
        require(baseMillis > 0) { "baseMillis must be positive" }
        require(maxMillis >= baseMillis) { "maxMillis must be at least baseMillis" }
        // The ceiling is computed by shifting, and a shift that overflows Long produces a negative
        // number, which `coerceAtMost` happily accepts as "smaller than the maximum" — a backoff
        // that hands back a negative delay. Refusing at construction beats discovering it on the
        // twentieth reconnect of a device in the field.
        require(baseMillis <= Long.MAX_VALUE shr EXPONENT_CAP) {
            "baseMillis $baseMillis overflows when shifted $EXPONENT_CAP times"
        }
    }

    private var attempt = 0

    /** How many failures have accumulated since the last [reset]. */
    val failures: Int get() = attempt

    /**
     * The delay to wait before the next attempt, and the cost of the failure that preceded it.
     *
     * The ceiling doubles per failure and is clamped at [maxMillis]; the returned delay is drawn
     * uniformly below it. The shift is computed on a bounded exponent because `1L shl 64` is `1L`
     * on the JVM rather than an overflow — a phone offline for a day would otherwise wrap round to
     * the base delay and start hammering, which is the opposite of what a backoff is for.
     */
    fun nextDelayMillis(): Long {
        // One mechanism, not two. An earlier version clamped the exponent here *and* returned
        // maxMillis directly above the cap, and deleting the clamp left every test green — the
        // second branch was covering for it. Whichever of the two was wrong, nothing could have
        // told us. The clamp is now the only thing standing between `attempt` and the shift.
        val exponent = attempt.coerceAtMost(EXPONENT_CAP)
        if (attempt < Int.MAX_VALUE) attempt++
        val ceiling = (baseMillis shl exponent).coerceAtMost(maxMillis)
        // nextLong is exclusive at the top, so the ceiling itself is never returned; +1 keeps the
        // cap reachable and keeps a base of 1ms from degenerating to a constant 0.
        return random.nextLong(ceiling + 1)
    }

    /** Called when a stream has actually been established. */
    fun reset() {
        attempt = 0
    }

    private companion object {
        // 2^20 * 1s is already twelve days, far beyond any sane cap, and it is nowhere near the
        // 63-bit shift width — which is the point. `shl` on a Long uses the low six bits of its
        // operand, so `1000L shl 64` is `1000L`, not an overflow and not an error: a device that
        // had failed to reconnect 64 times would compute the *shortest* delay in the schedule and
        // start hammering the server it had just spent an hour failing to reach.
        const val EXPONENT_CAP = 20
    }
}
