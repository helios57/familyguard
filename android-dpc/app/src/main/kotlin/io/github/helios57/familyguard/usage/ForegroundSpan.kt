package io.github.helios57.familyguard.usage

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * One stretch of one package being in the foreground, as the platform timestamped it.
 *
 * Wall-clock milliseconds, because that is the only thing `UsageStatsManager` reports. What keeps
 * FR-3.2 true is not this type but [UsageTracker], which never credits a window with more time than
 * the monotonic clock says has passed — see the budget argument on [UsageLedger.add].
 */
data class ForegroundSpan(
    val packageName: String,
    val startMillis: Long,
    val endMillis: Long,
) {
    val durationMillis: Long get() = (endMillis - startMillis).coerceAtLeast(0)

    /** The part of this span that lies inside `[fromMillis, toMillis]`, or null if none does. */
    fun clampedTo(fromMillis: Long, toMillis: Long): ForegroundSpan? {
        val start = maxOf(startMillis, fromMillis)
        val end = minOf(endMillis, toMillis)
        return if (end > start) copy(startMillis = start, endMillis = end) else null
    }
}

/**
 * An app that was in the foreground when a window ended and had not left it.
 *
 * **This type exists because the platform does not repeat itself.**
 * `UsageStatsManager.queryEvents(from, to)` returns the transitions inside that window and nothing
 * else, so an app that was already in the foreground when the window opened and still there when it
 * closed emits no event at all. A fold that treated each window independently therefore saw an empty
 * stream and credited nothing — measured before this type existed: **4 minutes for a 29-minute
 * session**, so switching apps was measured and sitting still was not.
 *
 * [startMillis] is the *real* start, which is usually before the window that reports it. Two
 * consumers want two different things from that, and conflating them is how this would go wrong
 * again: the day totals want only the part inside the window just measured (see
 * [ForegroundSpan.clampedTo]), while a record of what ran when wants the true start.
 */
data class OpenSpan(val packageName: String, val startMillis: Long)

/**
 * What one window of platform events folded to: the sessions that ended in it, and the one that did
 * not.
 *
 * [closed] carries true starts, so a session seeded from a previous window reports the moment the
 * app was actually opened rather than the poll boundary it survived.
 */
data class ForegroundWindow(
    val closed: List<ForegroundSpan>,
    val open: OpenSpan?,
)

/**
 * Splits spans at local midnight and totals them per day and package.
 *
 * The split is the point. A poll that runs at 00:02 covers a window that began the previous day, and
 * attributing the whole of it to the day the poll happened to land in moves screen time across the
 * quota boundary — in the direction that gives a child a fresh allowance while yesterday's minutes
 * are still being used. Every span is therefore cut at the first midnight it crosses, and each piece
 * is credited to its own day.
 *
 * The zone is the *policy's*, not the device's: the server keys usage by
 * `enforce.DayKey(policy, now)`, and a device sitting in a different timezone from the family's
 * would otherwise post day keys the server's quota never reads. Midnight is resolved through
 * `atStartOfDay`, so a day that begins at 01:00 because of a DST transition is cut at 01:00.
 */
object DayAttribution {

    private val DAY: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd")

    /**
     * The day key for an instant, in the same format and the same zone as [byDay] produces.
     *
     * One function rather than a second `yyyy-MM-dd` somewhere else: the device posts totals under
     * these keys and reads its own back to enforce the quota, and two formatters that agree today
     * are two that can disagree later — silently, because a mismatched key reads as a day with no
     * usage rather than as an error.
     */
    fun key(atMillis: Long, zone: ZoneId): String =
        Instant.ofEpochMilli(atMillis).atZone(zone).toLocalDate().format(DAY)

    /**
     * The policy's zone, or null when it cannot be read.
     *
     * Null rather than a fallback to the device's own zone. A device travelling — or one whose
     * timezone a child changed — would otherwise attribute usage to a day the server's quota never
     * reads, and the effect is a quota that is never reached. `EnforcementEngine.compute` refuses
     * the same input outright, which is the loud half; this is the half that must not guess.
     */
    fun zoneOf(timezone: String): ZoneId? =
        runCatching { ZoneId.of(timezone.trim()) }.getOrNull()

    fun byDay(spans: List<ForegroundSpan>, zone: ZoneId): Map<String, Map<String, Long>> {
        val out = sortedMapOf<String, MutableMap<String, Long>>()
        for (span in spans) {
            if (span.packageName.isBlank() || span.durationMillis <= 0) continue
            var cursor = span.startMillis
            while (cursor < span.endMillis) {
                val date = Instant.ofEpochMilli(cursor).atZone(zone).toLocalDate()
                val nextMidnight = date.plusDays(1).atStartOfDay(zone).toInstant().toEpochMilli()
                // Defensive: a zone whose next start-of-day does not advance would spin here
                // forever, and a device that never returns from a usage poll is a device that stops
                // enforcing anything.
                if (nextMidnight <= cursor) break
                val end = minOf(span.endMillis, nextMidnight)
                val day = date.format(DAY)
                val bucket = out.getOrPut(day) { linkedMapOf() }
                bucket[span.packageName] = (bucket[span.packageName] ?: 0L) + (end - cursor)
                cursor = end
            }
        }
        return out
    }
}
