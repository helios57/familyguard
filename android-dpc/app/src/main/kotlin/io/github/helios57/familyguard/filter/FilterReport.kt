package io.github.helios57.familyguard.filter

import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

/**
 * What the phone tells the server about its own filter, for one heartbeat.
 *
 * Separate from [FilterListState] because the two answer different questions. The store's state is
 * what is on disk; this is what the parent is shown — and the difference is entirely about which
 * values must be **null**. A console that shows "0 rules" for a phone that has never fetched a list
 * is showing a measurement that was never taken, which is the one thing this project treats as
 * worse than showing nothing (FR-6.10).
 *
 * Pure on purpose: every decision about nulls is here, where a fixture can drive it, rather than in
 * `ConnectionService.telemetry()`, where nothing can.
 */
data class FilterReport(
    /** Rules compiled and loaded, or null when no list has ever been fetched. */
    val rules: Int?,
    /** When that list was fetched, RFC3339, or null when there is none. */
    val fetchedAt: String?,
    /** Whether a tunnel is up right now, or null on a build with no filter at all. */
    val running: Boolean?,
    /**
     * Why no tunnel is running, or "" when there is nothing to explain (FR-6.11).
     *
     * Null on a build with no filter, like the three above. Blank whenever [running] is true, and
     * that is asserted rather than assumed: the reason and the state it explains are reported on
     * the same heartbeat, so a console that showed both could otherwise say "the tunnel is up" and
     * "the network named no resolver" in the same breath.
     */
    val reason: String?,
) {
    companion object {

        /**
         * Nothing to report.
         *
         * Not "off": a Play build carries no filter, so `running = false` there would be a claim
         * about a switch that does not exist. Three nulls clear the server's columns instead.
         */
        val NOT_AVAILABLE = FilterReport(rules = null, fetchedAt = null, running = null, reason = null)

        private val RFC3339: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")

        fun of(
            available: Boolean,
            list: FilterListState,
            running: Boolean?,
            reason: String? = null,
        ): FilterReport {
            if (!available) return NOT_AVAILABLE
            return FilterReport(
                // A list that has never been fetched reports nothing rather than zero. The store
                // refuses a list that compiles to no rules, so a real zero cannot occur and a
                // reported zero could only mean "never measured" — exactly the ambiguity this
                // avoids.
                rules = if (list.isEmpty()) null else list.rules,
                fetchedAt = at(list.fetchedAt),
                running = running,
                // A tunnel that is up has nothing to explain, whatever the service last recorded:
                // the two are read one after the other and a stand-down that has since been fixed
                // would otherwise arrive alongside the tunnel that fixed it.
                reason = if (running == true) "" else reason.orEmpty(),
            )
        }

        /** [millis] as RFC3339 in UTC, or null when the clock never recorded anything. */
        fun at(millis: Long): String? {
            if (millis <= 0) return null
            return OffsetDateTime.ofInstant(Instant.ofEpochMilli(millis), ZoneOffset.UTC).format(RFC3339)
        }
    }
}
