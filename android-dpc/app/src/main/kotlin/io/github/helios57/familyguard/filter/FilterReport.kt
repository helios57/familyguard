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
     *
     * **Never blank while the filter is not running.** A phone that reports "off" and says nothing
     * has told the parent less than nothing: the console then guesses, and on 2026-09-20 its guess
     * was measurably wrong on both counts. A service that has recorded no decision at all — and a
     * service whose record is the empty string, which is the same silence with a different spelling
     * — says so in words ([NOTHING_RECORDED]) instead.
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

        /**
         * What a filter that is not running reports when the service has recorded no decision.
         *
         * The case this covers is a service that never ran: the sync asked for it, the platform did
         * not bring it up, and nothing in this process took a decision to describe. That used to
         * report "", which is the value that means "nothing to explain" — so the one state with a
         * cause nobody could see was reported as the state with no cause at all.
         */
        const val NOTHING_RECORDED = "the filter has not started on this phone"

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
                // Three cases, and the middle one is the one that was missing.
                //
                // A tunnel that is up has nothing to explain, whatever the service last recorded:
                // the two are read one after the other and a stand-down that has since been fixed
                // would otherwise arrive alongside the tunnel that fixed it. A tunnel that is down
                // with words for it sends them. A tunnel that is down with NO words is the state
                // that used to be reported as "", i.e. as if there were nothing to say — and it is
                // the only one whose cause is outside this process entirely.
                reason = when {
                    running == true -> ""
                    !reason.isNullOrBlank() -> reason
                    else -> NOTHING_RECORDED
                },
            )
        }

        /** [millis] as RFC3339 in UTC, or null when the clock never recorded anything. */
        fun at(millis: Long): String? {
            if (millis <= 0) return null
            return OffsetDateTime.ofInstant(Instant.ofEpochMilli(millis), ZoneOffset.UTC).format(RFC3339)
        }
    }
}
