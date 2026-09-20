package io.github.helios57.familyguard.usage

import java.time.ZoneId

/** What one poll of the usage tracker produced. */
sealed interface UsageTick {

    /**
     * Cumulative totals for every day the window touched, in milliseconds per package.
     *
     * Cumulative and not just this window's delta, because that is what the server's upsert needs —
     * see [UsageLedger].
     */
    data class Measured(val byDay: Map<String, Map<String, Long>>) : UsageTick

    /** The screen was off for the whole window, or this is the first poll. Nothing to report. */
    data object Idle : UsageTick

    /**
     * Screen time could not be measured. **Never reported as zero usage**, because zero is a
     * measurement and this is the absence of one — and the difference between them is a daily quota
     * that works and one that can never be reached.
     */
    data class NotMeasured(val reason: String) : UsageTick
}

/**
 * Per-package foreground time: measured, budgeted against a monotonic clock, credited to a day.
 *
 * The three collaborators split the job so that everything with a rule in it can be tested on the
 * JVM: [ForegroundReader] is the platform, [ScreenOnClock] is the monotonic budget, [UsageLedger] is
 * the arithmetic and the persistence, and this class is the sequence they run in.
 *
 * FR-3.2 lives in one line here — the budget passed to [UsageLedger.add] comes from the monotonic
 * clock, never from the difference between two wall-clock readings — and FR-3.3 lives in the fact
 * that the budget is zero while the screen is off, so a window with the screen off credits nothing
 * whatever the platform reports about it.
 */
class UsageTracker(
    private val reader: ForegroundReader,
    private val ledger: UsageLedger,
    /**
     * Where the sittings go (FR-3.7).
     *
     * A separate collaborator from [ledger] because they answer different questions from the same
     * fold, and they disagree about the one thing that matters most here: the ledger wants the part
     * of a session that fell inside the window it is crediting, and this wants the session.
     */
    private val sessions: SessionLog,
    private val screen: ScreenOnClock,
    /** The *policy's* timezone, so day keys match the ones the server's quota reads. */
    private val zone: () -> ZoneId,
    private val wallClock: () -> Long,
    private val monotonicClock: () -> Long,
) {

    private var windowStart: Long? = null

    /**
     * The app that was in the foreground when the last window closed, if any.
     *
     * In memory only, and dropped whenever the next window is not contiguous with the one that
     * produced it — which is every window after a process restart, because [windowStart] is in
     * memory too. That is the deliberate bound: a carry that survived a restart would have to be
     * trusted across hours nobody observed, and the session it reported would run from whenever the
     * service died to whenever the child next touched the phone. Losing the open session at a
     * restart costs the minutes until the next app switch; keeping it could invent a night.
     */
    private var carried: OpenSpan? = null

    /** The screen went on or off; [atMonotonicMillis] must come from the monotonic clock. */
    fun onScreenOn(atMonotonicMillis: Long) {
        screen.onScreenOn(atMonotonicMillis)
    }

    fun onScreenOff(atMonotonicMillis: Long) {
        screen.onScreenOff(atMonotonicMillis)
    }

    /** Whether the screen is on as far as this tracker has been told. */
    fun screenIsOn(): Boolean = screen.isScreenOn()

    /** What this device has already measured for [day], for a report that has to be re-sent. */
    fun totalsFor(day: String): Map<String, Long> = ledger.totals(day)

    fun tick(): UsageTick {
        val now = wallClock()
        val budget = screen.drain(monotonicClock())
        val from = windowStart
        windowStart = now

        // The first poll establishes where the next window begins and reports nothing. Anything
        // else would have to invent a start — most naturally "an hour ago" — and credit a child
        // with time this device never watched.
        if (from == null) return UsageTick.Idle

        if (now <= from) {
            // The wall clock moved backwards or stood still, so the query window is meaningless.
            // The budget for it has already been drained, which is deliberate: that time is spent,
            // not owed, and carrying it into the next window would let a clock nudged backwards
            // repeatedly build up a budget to spend later.
            //
            // The carry goes with it: it is anchored to a wall-clock instant, and the next window
            // will not be contiguous with it in any sense that can be reasoned about.
            carried = null
            return UsageTick.NotMeasured("the wall clock did not advance between polls")
        }
        if (budget <= 0) return UsageTick.Idle

        val window = reader.read(from, now, carried) ?: run {
            // Not measured is not zero, and it is also not a session that kept running. Whatever
            // was open stopped being observable here, so it is dropped rather than resumed later
            // against a window nobody looked at.
            carried = null
            return UsageTick.NotMeasured(reader.unavailableReason())
        }
        carried = window.open

        // The sittings, with their TRUE starts, before the clamping below throws them away. A span
        // reaches `closed` exactly once — in the window it ends in, however many windows it spanned
        // — so recording here cannot draw one sitting twice.
        //
        // Deliberately NOT clamped and NOT scaled by the budget. The budget is the quota's ceiling
        // against a tampered clock (FR-3.2), and scaling an interval by it would produce a sitting
        // that never happened: a timeline is a record of what the platform observed, and the quota
        // stays the ledger's arithmetic rather than this one's.
        sessions.record(window.closed, now)

        // Two different questions, one fold. The day totals want the time that fell inside *this*
        // window — a session seeded from the previous one has already been credited up to `from`,
        // and crediting its true start again would double-count every poll it survives. What a
        // session RECORD wants is the opposite (the true start), which is why `window.closed` keeps
        // it and the clamping happens here rather than in the fold.
        val measured = window.closed.mapNotNull { it.clampedTo(from, now) } +
            listOfNotNull(window.open?.let { ForegroundSpan(it.packageName, maxOf(it.startMillis, from), now) })

        val byDay = DayAttribution.byDay(measured, zone())
        val changed = ledger.add(byDay, budget)
        if (changed.isEmpty()) return UsageTick.Idle
        return UsageTick.Measured(changed.associateWith { ledger.totals(it) })
    }
}
