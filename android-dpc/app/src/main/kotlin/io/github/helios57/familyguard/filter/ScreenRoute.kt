package io.github.helios57.familyguard.filter

/**
 * The route the tunnel runs with, given the screen (FR-26.4).
 *
 * With the screen on the parent's choice holds. Five minutes after it goes off, with nothing playing,
 * a FULL route narrows to DNS only: nobody is looking at an advert, and relaying every packet of the
 * phone's background traffic in user space is most of what the filter costs. Sound playing delays
 * the narrowing — music and podcasts carry audio adverts, a call is a call, and rebuilding the tunnel
 * would cut the stream — but sound starting after it does not widen the route again, for the same
 * reason: the rebuild would cut the stream that just started. So it narrows at most once per
 * screen-off, and the screen coming on restores FULL at once. A parent's DNS_ONLY is never widened.
 */
object ScreenRoute {
    const val DNS_ONLY_AFTER_MILLIS = 5 * 60 * 1000L

    /**
     * [screenOffAtElapsed] null with the screen off is "off since before the service knew", i.e. long
     * ago. Times are `elapsedRealtime`, which counts through sleep. [running] is the route the tunnel
     * runs now, or null when none is up.
     */
    fun effective(
        chosen: RouteMode,
        screenOn: Boolean,
        screenOffAtElapsed: Long?,
        nowElapsed: Long,
        audioActive: Boolean,
        running: RouteMode?,
    ): RouteMode = when {
        chosen == RouteMode.DNS_ONLY -> RouteMode.DNS_ONLY
        screenOn -> RouteMode.FULL
        running == RouteMode.DNS_ONLY -> RouteMode.DNS_ONLY
        audioActive -> RouteMode.FULL
        screenOffAtElapsed != null && nowElapsed - screenOffAtElapsed < DNS_ONLY_AFTER_MILLIS -> RouteMode.FULL
        else -> RouteMode.DNS_ONLY
    }

    /** When [effective] next changes with no event to say so, or null when only an event can change it. */
    fun nextChangeAtElapsed(
        chosen: RouteMode,
        screenOn: Boolean,
        screenOffAtElapsed: Long?,
        nowElapsed: Long,
        audioActive: Boolean,
    ): Long? {
        if (chosen == RouteMode.DNS_ONLY || screenOn || audioActive || screenOffAtElapsed == null) return null
        val at = screenOffAtElapsed + DNS_ONLY_AFTER_MILLIS
        return at.takeIf { it > nowElapsed }
    }
}
