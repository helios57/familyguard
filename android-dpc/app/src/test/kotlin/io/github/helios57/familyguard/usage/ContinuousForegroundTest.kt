package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

/**
 * An app left in the foreground across several polls, measured through the real [SpanFolder].
 *
 * **Every other usage test stubs [ForegroundReader] with ready-made spans, and that is what hid
 * this.** The platform does not hand out spans; it hands out transitions, and
 * `UsageStatsManager.queryEvents(from, to)` returns only the ones that fall *inside* the window. An
 * app that is already in the foreground when a window opens and still there when it closes emits
 * nothing at all — no `RESUMED`, no `PAUSED` — so a fold over that window sees an empty stream.
 *
 * A double that answers with spans can never be asked that question. [EventStreamReader] below is
 * therefore as unhelpful as the platform is: it holds the event stream and slices it by window, and
 * the slicing is the whole point of the fixture.
 *
 * What the defect cost, measured before the fix: **4 minutes credited for a 29-minute session**.
 * Only the tail of the window containing the `RESUMED` was counted, so switching apps was measured
 * and sitting still was not — and `usedMinutesToday` is what a daily limit is enforced against.
 */
class ContinuousForegroundTest {

    private val zurich = ZoneId.of("Europe/Zurich")
    private val minute = 60_000L
    private val day = "2026-08-17"

    private var wall = at("2026-08-17T10:00+02:00")
    private var monotonic = 0L

    private val store = InMemoryUsageStore()
    private val ledger = UsageLedger(store)
    private val screen = ScreenOnClock(screenOn = true, startMillis = 0)

    private fun trackerOver(events: List<ForegroundEvent>) = UsageTracker(
        reader = EventStreamReader(events),
        ledger = ledger,
        screen = screen,
        zone = { zurich },
        wallClock = { wall },
        monotonicClock = { monotonic },
    )

    /**
     * The regression. One `RESUMED`, no `PAUSED`, six five-minute polls.
     *
     * 29 rather than 30 because the first poll establishes the window and reports nothing, so
     * measurement starts at 10:00 and the app was opened at 10:01.
     */
    @Test
    fun `an app left open across six polls is credited every minute of it`() {
        val tracker = trackerOver(listOf(resumed(VIDEO, "2026-08-17T10:01+02:00")))
        tracker.tick()

        repeat(6) { advance(tracker, minutes = 5) }

        assertEquals(
            "minutes credited for a 29-minute continuous session",
            29L,
            tracker.totalsFor(day).getValue(VIDEO) / minute,
        )
    }

    /** The other half: time after the app is closed must not keep accruing. */
    @Test
    fun `an app closed inside a window stops being credited there`() {
        val tracker = trackerOver(
            listOf(
                resumed(VIDEO, "2026-08-17T10:01+02:00"),
                paused(VIDEO, "2026-08-17T10:12+02:00"),
            ),
        )
        tracker.tick()

        repeat(6) { advance(tracker, minutes = 5) }

        assertEquals(11L, tracker.totalsFor(day).getValue(VIDEO) / minute)
    }

    /** Two apps, one handover, no pause between them — the second closes the first (FR-3.1). */
    @Test
    fun `a handover splits the time between the two apps`() {
        val tracker = trackerOver(
            listOf(
                resumed(VIDEO, "2026-08-17T10:01+02:00"),
                resumed(GAME, "2026-08-17T10:16+02:00"),
            ),
        )
        tracker.tick()

        repeat(6) { advance(tracker, minutes = 5) }

        assertEquals(15L, tracker.totalsFor(day).getValue(VIDEO) / minute)
        assertEquals(14L, tracker.totalsFor(day).getValue(GAME) / minute)
    }

    /**
     * FR-3.3, which the carry must not weaken: the screen going off closes the session where it went
     * off, and the app is not credited for the dark minutes even though it is still "open".
     */
    @Test
    fun `the screen going off ends the session at the moment it went off`() {
        val tracker = trackerOver(
            listOf(
                resumed(VIDEO, "2026-08-17T10:01+02:00"),
                screenOff("2026-08-17T10:07+02:00"),
            ),
        )
        tracker.tick()
        advance(tracker, minutes = 5)

        // The screen went off six minutes in; the clock only budgets what was lit.
        tracker.onScreenOff(monotonic + 2 * minute)
        monotonic += 2 * minute
        wall += 2 * minute
        advance(tracker, minutes = 5)

        assertEquals(6L, tracker.totalsFor(day).getValue(VIDEO) / minute)
    }

    /**
     * FR-3.2 as a negative control. The carry adds time the platform never reported an event for, so
     * the budget ceiling is the thing most at risk from it: a wall clock shoved forward an hour must
     * still credit only the minutes the monotonic clock agrees passed.
     */
    @Test
    fun `a wall clock pushed forward is still bounded by the monotonic budget`() {
        val tracker = trackerOver(listOf(resumed(VIDEO, "2026-08-17T10:01+02:00")))
        tracker.tick()

        wall += 60 * minute
        monotonic += 5 * minute
        tracker.tick()

        assertEquals(
            "the hour the wall clock invented is not screen time",
            5L,
            tracker.totalsFor(day).getValue(VIDEO) / minute,
        )
    }

    private fun advance(tracker: UsageTracker, minutes: Int): UsageTick {
        wall += minutes * minute
        monotonic += minutes * minute
        return tracker.tick()
    }

    private fun at(text: String): Long = ZonedDateTime.parse(text).toInstant().toEpochMilli()

    private fun resumed(pkg: String, at: String) =
        ForegroundEvent(ForegroundEventKind.RESUMED, pkg, at(at))

    private fun paused(pkg: String, at: String) =
        ForegroundEvent(ForegroundEventKind.PAUSED, pkg, at(at))

    private fun screenOff(at: String) =
        ForegroundEvent(ForegroundEventKind.SCREEN_OFF, "", at(at))

    /**
     * A reader that is exactly as unhelpful as `UsageStatsManager`.
     *
     * It holds the whole event stream and hands the fold only the events inside the asked-for
     * window, which is what `queryEvents` does. A double that returned the whole stream every time
     * would make this file pass over the defect it exists to pin.
     */
    private class EventStreamReader(private val events: List<ForegroundEvent>) : ForegroundReader {

        override fun read(fromMillis: Long, toMillis: Long, carried: OpenSpan?): ForegroundWindow? =
            SpanFolder.fold(
                events.filter { it.atMillis >= fromMillis && it.atMillis < toMillis },
                windowEndMillis = toMillis,
                carried = carried,
            )

        override fun unavailableReason(): String = "the fake reader always measures"
    }

    private companion object {
        const val VIDEO = "com.example.video"
        const val GAME = "com.example.game"
    }
}
