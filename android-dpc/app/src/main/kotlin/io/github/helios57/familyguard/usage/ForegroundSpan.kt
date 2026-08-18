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
}

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
