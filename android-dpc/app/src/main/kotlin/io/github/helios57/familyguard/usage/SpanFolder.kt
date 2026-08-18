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
 * - **A span still open at the end of the window is closed there**, not carried, because the next
 *   window starts where this one ended and will open its own from its own `RESUMED`. Carrying would
 *   need state that survives a process death for the sake of at most one poll interval.
 */
object SpanFolder {

    fun fold(events: List<ForegroundEvent>, windowEndMillis: Long): List<ForegroundSpan> {
        val spans = mutableListOf<ForegroundSpan>()
        var openPackage: String? = null
        var openSince = 0L

        fun close(atMillis: Long) {
            val pkg = openPackage ?: return
            if (atMillis > openSince) spans += ForegroundSpan(pkg, openSince, atMillis)
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
        close(windowEndMillis)
        return spans
    }
}
