package io.github.helios57.familyguard.usage

/** The only three things about a platform usage event that matter here. */
enum class ForegroundEventKind { RESUMED, PAUSED, SCREEN_OFF }

data class ForegroundEvent(
    val kind: ForegroundEventKind,
    val packageName: String,
    val atMillis: Long,
)

/**
 * Turns a stream of resume/pause events into the spans between them.
 *
 * Kept apart from the platform reader, and pure, because this is where the arithmetic lives and the
 * platform half is a loop over a cursor. Every rule below is a way a real event stream is untidy:
 *
 * - **One package is in the foreground at a time**, so a `RESUMED` closes whatever was open. Android
 *   does not promise a `PAUSED` before the next `RESUMED`, and a stream that lost one would
 *   otherwise leave a span open until the end of the window and credit an app that stopped hours ago.
 * - **A `PAUSED` for a package that is not the open one is stale** and is dropped. Activities within
 *   one app pause and resume around each other; taking any pause as the end of the session would cut
 *   a long session into a short one every time the user opened a second screen inside the same app.
 * - **Screen off closes the open span** (FR-3.3), at the moment the screen went off rather than at
 *   the end of the window.
 * - **A span still open at the end of the window is HANDED BACK, not closed** — see [carried].
 *
 * **That last rule was the opposite until 2026-09-20, and it cost most of the measurement.** The
 * reasoning it replaced was *"the next window starts where this one ended and will open its own span
 * from its own `RESUMED`"*, and that is simply not how `UsageStatsManager.queryEvents(from, to)`
 * behaves: it reports transitions, and an app that stays in the foreground makes none. So the next
 * window saw an empty stream and folded it to nothing. Measured over six consecutive five-minute
 * polls with one `RESUMED` and no `PAUSED`: **4 minutes credited for a 29-minute session.** The
 * shape of the error is worse than its size — app-switching was measured and sitting still was not,
 * so a child reading one book or watching one video all afternoon registered almost no screen time
 * and no daily limit could be reached.
 */
object SpanFolder {

    /**
     * @param carried what the previous window left in the foreground, or null on the first window
     *   and whenever the previous window could not be measured. It must be contiguous with this one
     *   — [UsageTracker] drops it otherwise, because a carry across a gap would report a session
     *   running through hours nobody observed.
     */
    fun fold(
        events: List<ForegroundEvent>,
        windowEndMillis: Long,
        carried: OpenSpan? = null,
    ): ForegroundWindow {
        val closed = mutableListOf<ForegroundSpan>()
        var openPackage: String? = carried?.packageName
        var openSince: Long = carried?.startMillis ?: 0L

        fun close(atMillis: Long) {
            val pkg = openPackage ?: return
            if (atMillis > openSince) closed += ForegroundSpan(pkg, openSince, atMillis)
            openPackage = null
        }

        // Sorted rather than trusted: `queryEvents` returns in timestamp order today, and a fold that
        // silently produced negative-length spans if it ever did not would show up as usage quietly
        // going missing, which is the hardest kind of wrong to notice.
        for (event in events.sortedBy { it.atMillis }) {
            when (event.kind) {
                ForegroundEventKind.RESUMED -> {
                    if (event.packageName.isBlank()) continue
                    close(event.atMillis)
                    openPackage = event.packageName
                    openSince = event.atMillis
                }
                ForegroundEventKind.PAUSED ->
                    if (event.packageName == openPackage) close(event.atMillis)
                ForegroundEventKind.SCREEN_OFF -> close(event.atMillis)
            }
        }

        // Deliberately NOT closed at the window end. An app still in the foreground has not ended a
        // session, and saying it has both truncates the measurement and invents a session boundary
        // that the child never made.
        val open = openPackage?.let { OpenSpan(it, openSince) }
        return ForegroundWindow(closed, open)
    }
}
