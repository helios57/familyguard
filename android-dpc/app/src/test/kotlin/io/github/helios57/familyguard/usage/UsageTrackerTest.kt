package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

/**
 * The sequence a poll runs in, and the four answers it can give.
 *
 * The distinction that carries the most weight is [UsageTick.NotMeasured] against [UsageTick.Idle].
 * Idle is a measurement — the screen was off, or nothing ran — and reporting it as zero is correct.
 * NotMeasured is the absence of one, and reporting *that* as zero makes every daily quota unreachable
 * while the console shows a healthy device and a well-behaved child (FR-3.4).
 */
class UsageTrackerTest {

    private val zurich = ZoneId.of("Europe/Zurich")
    private val minute = 60_000L

    private val reader = FakeReader()
    private val store = InMemoryUsageStore()
    private val ledger = UsageLedger(store)
    private val screen = ScreenOnClock(screenOn = true, startMillis = 0)

    private var wall = at("2026-08-17T10:00+02:00")
    private var monotonic = 0L
    private var zone = zurich

    private val tracker = UsageTracker(
        reader = reader,
        ledger = ledger,
        screen = screen,
        zone = { zone },
        wallClock = { wall },
        monotonicClock = { monotonic },
    )

    /**
     * The first poll has no window behind it. Anything other than "nothing to report" would have to
     * invent a start — most naturally "an hour ago" — and credit a child with time never watched.
     */
    @Test
    fun `the first poll establishes the window and reports nothing`() {
        assertEquals(UsageTick.Idle, tracker.tick())
        assertTrue(ledger.days().isEmpty())
    }

    @Test
    fun `a measured window reports cumulative totals for the days it touched`() {
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))

        val tick = advance(minutes = 30)

        val measured = tick as UsageTick.Measured
        assertEquals(setOf("2026-08-17"), measured.byDay.keys)
        assertEquals(20 * minute, measured.byDay.getValue("2026-08-17").getValue(GAME))
    }

    /** What the report carries is the day's running total, not this window's delta. */
    @Test
    fun `a second window reports the day total, not the increment`() {
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))
        advance(minutes = 30)
        reader.spans = listOf(span(GAME, "2026-08-17T10:30+02:00", "2026-08-17T10:35+02:00"))

        val tick = advance(minutes = 30)

        assertEquals(25 * minute, (tick as UsageTick.Measured).byDay.getValue("2026-08-17").getValue(GAME))
    }

    @Test
    fun `a window whose spans cross midnight reports both days`() {
        wall = at("2026-08-17T23:50+02:00")
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-17T23:50+02:00", "2026-08-18T00:10+02:00"))

        val tick = advance(minutes = 30)

        val measured = tick as UsageTick.Measured
        assertEquals(setOf("2026-08-17", "2026-08-18"), measured.byDay.keys)
        assertEquals(10 * minute, measured.byDay.getValue("2026-08-17").getValue(GAME))
        assertEquals(10 * minute, measured.byDay.getValue("2026-08-18").getValue(GAME))
    }

    /** The day keys follow the *policy's* zone, which is the one the server's quota reads. */
    @Test
    fun `the day key follows the zone the tracker is given`() {
        zone = ZoneId.of("UTC")
        wall = at("2026-08-18T00:05+02:00")
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-18T00:05+02:00", "2026-08-18T00:15+02:00"))

        val tick = advance(minutes = 30)

        assertEquals(setOf("2026-08-17"), (tick as UsageTick.Measured).byDay.keys)
    }

    // ---- idle, which is a measurement -----------------------------------------------------------

    /** FR-3.3. Nothing the platform says about a window with the screen off can be credited. */
    @Test
    fun `a window with the screen off the whole time is idle`() {
        tracker.tick()
        tracker.onScreenOff(0)
        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))

        assertEquals(UsageTick.Idle, advance(minutes = 30))
        assertTrue("nothing was credited", ledger.totals("2026-08-17").isEmpty())
    }

    @Test
    fun `a window in which nothing ran is idle, not a measurement of zero`() {
        tracker.tick()
        reader.spans = emptyList()

        assertEquals(UsageTick.Idle, advance(minutes = 30))
    }

    // ---- not measured, which is not zero --------------------------------------------------------

    @Test
    fun `a reader that cannot see the foreground is not measured, with its reason`() {
        tracker.tick()
        reader.spans = null
        reader.reason = "usage access has not been granted"

        val tick = advance(minutes = 30)

        assertEquals(UsageTick.NotMeasured("usage access has not been granted"), tick)
    }

    /**
     * A wall clock that did not advance makes the query window meaningless. The budget for it has
     * already been drained, deliberately: that time is spent, not owed, so a clock nudged backwards
     * repeatedly cannot build up a budget to spend later.
     */
    @Test
    fun `a wall clock that went backwards is not measured`() {
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))

        wall -= 5 * minute
        monotonic += 30 * minute
        val tick = tracker.tick()

        assertTrue(tick is UsageTick.NotMeasured)
        assertTrue("nothing was credited", ledger.totals("2026-08-17").isEmpty())
    }

    @Test
    fun `the drained budget is not carried into the window after a backwards clock`() {
        tracker.tick()
        wall -= 5 * minute
        monotonic += 30 * minute
        tracker.tick()

        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))
        wall = at("2026-08-17T11:00+02:00")
        monotonic += 2 * minute
        val tick = tracker.tick()

        assertEquals(
            "credited by the two minutes of the new window, not by the discarded thirty",
            2 * minute,
            (tick as UsageTick.Measured).byDay.getValue("2026-08-17").getValue(GAME),
        )
    }

    // ---- the plumbing the service uses ----------------------------------------------------------

    @Test
    fun `the screen state is reported from the clock`() {
        assertTrue(tracker.screenIsOn())
        tracker.onScreenOff(minute)
        assertFalse(tracker.screenIsOn())
        tracker.onScreenOn(2 * minute)
        assertTrue(tracker.screenIsOn())
    }

    @Test
    fun `totals for a day can be read back for a report that has to be re-sent`() {
        tracker.tick()
        reader.spans = listOf(span(GAME, "2026-08-17T10:00+02:00", "2026-08-17T10:20+02:00"))
        advance(minutes = 30)

        assertEquals(20 * minute, tracker.totalsFor("2026-08-17").getValue(GAME))
        assertTrue(tracker.totalsFor("2026-01-01").isEmpty())
    }

    @Test
    fun `the reader is asked for exactly the window between two polls`() {
        val start = wall
        tracker.tick()

        advance(minutes = 30)

        assertEquals(listOf(start to start + 30 * minute), reader.windows)
    }

    private fun advance(minutes: Int): UsageTick {
        wall += minutes * minute
        monotonic += minutes * minute
        return tracker.tick()
    }

    private fun at(text: String): Long = ZonedDateTime.parse(text).toInstant().toEpochMilli()

    private fun span(pkg: String, from: String, to: String) = ForegroundSpan(pkg, at(from), at(to))

    private class FakeReader : ForegroundReader {
        var spans: List<ForegroundSpan>? = emptyList()
        var reason: String = "not asked yet"
        val windows = mutableListOf<Pair<Long, Long>>()

        override fun spans(fromMillis: Long, toMillis: Long): List<ForegroundSpan>? {
            windows += fromMillis to toMillis
            return spans
        }

        override fun unavailableReason(): String = reason
    }

    private companion object {
        const val GAME = "com.example.game"
    }
}
