package io.github.helios57.familyguard.filter

/**
 * What to do about a tunnel that is up and carrying nothing.
 *
 * This is the "do not brick the phone" guarantee, and it exists because the platform's own safety
 * net has a hole exactly this shape. A `VpnService` started without lockdown is safe while it is
 * **down** — traffic simply goes around it. It is not safe while it is *up and broken*: every packet
 * the system hands it is one the filter is now responsible for, and a filter that answers none of
 * them is a phone with no internet, nothing on the screen saying why, and a child who can only
 * report *"the internet is weird"*. Nothing in Android notices that, because from the platform's
 * side the tunnel is running perfectly.
 *
 * So the tunnel watches itself, on one signal that cannot be argued with: **packets the apps sent
 * in, against packets this wrote back**. Not error counts — an error count is zero in the failure
 * that matters, where the code is looping or blocked rather than throwing.
 *
 * ### Two strikes, then stop trying
 *
 * A first failure is worth a restart: a socket that could not open, a network that changed
 * underneath, a resolver that went away. A second in a row is not — restarting into the same
 * failure forever is a phone that never works and a battery that never lasts, which is worse than
 * a filter that is simply off. [Verdict.STAND_DOWN] means stop, stay stopped, and let the next
 * policy change decide whether to try again. Failing off is always the right direction here: the
 * cost is an advertisement, and the cost of the other direction is a phone a parent cannot fix.
 */
class TunnelWatchdog(
    private val windowMillis: Long = WINDOW_MILLIS,
    private val minimumPackets: Long = MINIMUM_PACKETS,
    private val strikesBeforeStandDown: Int = 2,
) {

    enum class Verdict {
        /** Carrying traffic, or too quiet to tell. Nothing to do. */
        HEALTHY,

        /** Up and carrying nothing. Tear the tunnel down and build it again. */
        TEAR_DOWN,

        /** It has been rebuilt and is still carrying nothing. Stop, and stay stopped. */
        STAND_DOWN,
    }

    private var windowStart = Long.MIN_VALUE
    private var packetsInAtStart = 0L
    private var packetsOutAtStart = 0L
    private var strikes = 0

    /** How many windows in a row have gone by with nothing carried. Reported, never decided on. */
    fun strikes(): Int = strikes

    /**
     * Start, or restart, the window.
     *
     * Called when the tunnel comes up, and again after every [Verdict.TEAR_DOWN], because the
     * counters belong to the router that was just thrown away. Deliberately does **not** clear
     * [strikes]: that is what makes the second failure different from the first.
     */
    fun tunnelStarted(now: Long, packetsIn: Long = 0, packetsOut: Long = 0) {
        windowStart = now
        packetsInAtStart = packetsIn
        packetsOutAtStart = packetsOut
    }

    /** A new policy, or a parent switching the filter on again. Everything is forgiven. */
    fun reset() {
        strikes = 0
        windowStart = Long.MIN_VALUE
    }

    /**
     * One observation. Cheap enough to call on a timer, and it answers [Verdict.HEALTHY] until a
     * whole window has actually passed — a verdict taken over two seconds of a phone in a pocket
     * would stand the tunnel down for being idle.
     */
    fun sample(now: Long, packetsIn: Long, packetsOut: Long): Verdict {
        if (windowStart == Long.MIN_VALUE) {
            tunnelStarted(now, packetsIn, packetsOut)
            return Verdict.HEALTHY
        }
        if (now - windowStart < windowMillis) return Verdict.HEALTHY

        val arrived = packetsIn - packetsInAtStart
        val answered = packetsOut - packetsOutAtStart
        windowStart = now
        packetsInAtStart = packetsIn
        packetsOutAtStart = packetsOut

        // Too quiet to judge. A sleeping phone sends a handful of keepalives an hour, and reading
        // that as a broken tunnel would take the filter down every night.
        if (arrived < minimumPackets) {
            strikes = 0
            return Verdict.HEALTHY
        }
        if (answered > 0) {
            strikes = 0
            return Verdict.HEALTHY
        }
        strikes++
        return if (strikes >= strikesBeforeStandDown) Verdict.STAND_DOWN else Verdict.TEAR_DOWN
    }

    companion object {
        /**
         * Long enough that a brief stall is not a verdict, short enough that a child notices the
         * recovery rather than the outage.
         */
        const val WINDOW_MILLIS = 60_000L

        /**
         * Below this the window says nothing. A phone doing anything at all — one page, one chat
         * message — passes this in seconds; a phone in a pocket does not reach it in an hour.
         */
        const val MINIMUM_PACKETS = 100L
    }
}
