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
    private val screen: ScreenOnClock,
    /** The *policy's* timezone, so day keys match the ones the server's quota reads. */
    private val zone: () -> ZoneId,
    private val wallClock: () -> Long,
    private val monotonicClock: () -> Long,
) {

    private var windowStart: Long? = null

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
            return UsageTick.NotMeasured("the wall clock did not advance between polls")
        }
        if (budget <= 0) return UsageTick.Idle

        val spans = reader.spans(from, now) ?: return UsageTick.NotMeasured(reader.unavailableReason())
        val byDay = DayAttribution.byDay(spans, zone())
        val changed = ledger.add(byDay, budget)
        if (changed.isEmpty()) return UsageTick.Idle
        return UsageTick.Measured(changed.associateWith { ledger.totals(it) })
    }
}
