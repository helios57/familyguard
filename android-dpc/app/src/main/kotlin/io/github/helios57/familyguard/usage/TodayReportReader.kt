package io.github.helios57.familyguard.usage

import android.content.Context
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.TodayReport
import io.github.helios57.familyguard.sync.EncryptedPolicyCache
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter

/**
 * Today's report for FamilyGuard's own screen (FR-3.10), recomputed exactly the way the service
 * enforces: the cached policy, this phone's own measurement for the policy's day, and the engine.
 *
 * Recomputed rather than read from what the service last applied, because the screen must be right
 * when the service is not running — which is one of the reasons somebody opens it — and because the
 * engine is a pure function: the same input gives the same reasons the service acted on.
 */
object TodayReportReader {

    private val RFC3339: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")

    /** Null when this phone has never received a policy, or its zone cannot be read. */
    fun read(context: Context): TodayReport? {
        val input = EncryptedPolicyCache(context).load() ?: return null
        val zone = DayAttribution.zoneOf(input.settings.timezone) ?: return null
        val day = DayAttribution.key(System.currentTimeMillis(), zone)
        val byPackage = UsageLedger(EncryptedUsageStore(context)).totals(day)
            .mapValues { (_, ms) -> (ms / 60_000L).toInt() }
        val uncounted = UncountedPackages.on(context, input)
        val localCounted = byPackage.filterKeys { it !in uncounted }.values.sum()

        // The same combination the Synchronizer uses: max, never sum, per total and per package.
        val merged = (input.usedMinutesByPackage.keys + byPackage.keys).associateWith {
            maxOf(input.usedMinutesByPackage[it] ?: 0, byPackage[it] ?: 0)
        }
        val state = EnforcementEngine.compute(
            input.copy(
                now = OffsetDateTime.now(zone).format(RFC3339),
                usedMinutesToday = maxOf(input.usedMinutesToday, localCounted),
                usedMinutesByPackage = merged,
            ),
        )
        return TodayReport.of(input, state, merged, uncounted)
    }
}
