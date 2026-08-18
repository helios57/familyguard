package io.github.helios57.familyguard.usage

/**
 * How much time has passed with the screen on, measured monotonically.
 *
 * Every timestamp handed to this class must come from a clock that only moves forward and is not
 * settable — on the device that is `SystemClock.elapsedRealtime()`. `System.currentTimeMillis()` is
 * the wrong one twice: a child who can reach the date setting could inflate or erase a day's usage
 * with it, and NTP alone moves it backwards often enough to produce negative intervals.
 *
 * It is a small class on purpose. It is the only thing standing between "the platform said this app
 * was in the foreground for six hours" and a quota that believes it, and the whole of FR-3.3 is the
 * single rule that intervals with the screen off are not counted.
 */
class ScreenOnClock(
    screenOn: Boolean,
    startMillis: Long,
) {

    private var on: Boolean = screenOn
    private var since: Long = startMillis
    private var accumulated: Long = 0

    /** @return true when this changed the state; a repeated broadcast for the same state is a no-op. */
    fun onScreenOn(atMillis: Long): Boolean {
        if (on) return false
        on = true
        since = atMillis
        return true
    }

    /**
     * @return true when this changed the state.
     *
     * The screen-off broadcast is also where the interval that was running gets banked. Waiting for
     * the next drain instead would credit the whole of a night's sleep to whatever app was last in
     * the foreground.
     */
    fun onScreenOff(atMillis: Long): Boolean {
        if (!on) return false
        accumulated += interval(atMillis)
        on = false
        since = atMillis
        return true
    }

    /** Whether the screen is on as far as this clock has been told. */
    fun isScreenOn(): Boolean = on

    /**
     * The screen-on milliseconds since the previous drain, and resets the count.
     *
     * Draining while the screen is on leaves the open interval running from [atMillis], so no time
     * is counted twice and none is lost between two polls that both happen mid-session.
     */
    fun drain(atMillis: Long): Long {
        val banked = accumulated
        accumulated = 0
        val open = if (on) interval(atMillis) else 0
        if (on) since = atMillis
        return banked + open
    }

    /**
     * A monotonic clock cannot go backwards, so a negative interval means the caller passed
     * something else — `currentTimeMillis`, or a value captured before a reboot reset
     * `elapsedRealtime` to zero. Counting it as zero loses at most one window; treating it as
     * negative would silently subtract from a budget that is a ceiling on a child's screen time.
     */
    private fun interval(atMillis: Long): Long = (atMillis - since).coerceAtLeast(0)
}
