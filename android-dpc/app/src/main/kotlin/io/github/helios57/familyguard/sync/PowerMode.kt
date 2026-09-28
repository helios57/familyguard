package io.github.helios57.familyguard.sync

/**
 * Whether the phone keeps its connection to the server open (FR-26.1).
 *
 * - **ACTIVE**: the event stream is open, so a change reaches the phone within a second. While the
 *   screen is on, for a minute after it goes off, and for as long as Live runs (FR-27).
 * - **PASSIVE**: no stream. The phone wakes for a push, a safety poll, or the screen coming on
 *   (FR-26.2) — see [ConnectionService].
 *
 * The stream is not free to keep: its keepalive arrives every 20 s, and on mobile data each one holds
 * the radio in its high-power state for its tail, so an idle phone's radio was up about half the time.
 */
enum class PowerMode {
    ACTIVE,
    PASSIVE,
    ;

    companion object {
        /** A glance at the clock is not a mode change: closing and re-opening costs more than it saves. */
        const val GRACE_MILLIS = 60_000L

        /**
         * The mode for these facts. Times on the screen come from `elapsedRealtime`, which counts through
         * sleep and cannot be moved by a child; Live's end is the server's wall-clock instant.
         * [screenOffAtElapsed] null with the screen off is "off since before we knew", i.e. long ago.
         */
        fun decide(
            screenOn: Boolean,
            screenOffAtElapsed: Long?,
            nowElapsed: Long,
            liveUntilEpoch: Long,
            nowEpoch: Long,
        ): PowerMode = when {
            liveUntilEpoch > nowEpoch -> ACTIVE
            screenOn -> ACTIVE
            screenOffAtElapsed != null && nowElapsed - screenOffAtElapsed < GRACE_MILLIS -> ACTIVE
            else -> PASSIVE
        }

        /**
         * When, on the wall clock, [decide] can next change with no event to say so — the end of the
         * grace or the end of Live, whichever is first — or null when only an event can change it.
         */
        fun nextChangeAtEpoch(
            screenOn: Boolean,
            screenOffAtElapsed: Long?,
            nowElapsed: Long,
            liveUntilEpoch: Long,
            nowEpoch: Long,
        ): Long? {
            if (screenOn) return null
            val candidates = mutableListOf<Long>()
            if (screenOffAtElapsed != null) {
                val left = GRACE_MILLIS - (nowElapsed - screenOffAtElapsed)
                if (left > 0) candidates += nowEpoch + left
            }
            if (liveUntilEpoch > nowEpoch) candidates += liveUntilEpoch
            return candidates.minOrNull()
        }
    }
}
