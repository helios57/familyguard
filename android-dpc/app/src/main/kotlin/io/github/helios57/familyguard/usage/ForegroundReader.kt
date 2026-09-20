package io.github.helios57.familyguard.usage

/**
 * The platform's record of what was in the foreground, as far as this app can see it.
 *
 * One interface so the arithmetic can be tested on the JVM against a stream of events, and so the
 * one implementation that touches Android is a loop over a cursor with no rules in it.
 */
interface ForegroundReader {

    /**
     * Folds the platform's events for `[fromMillis, toMillis)`, or null if nothing could be measured.
     *
     * Null is never "no usage". A missing usage-access grant, or a platform that throws, returns
     * nothing from every query — and reporting that as zero shows a parent a child who spent the day
     * off their phone while making the daily limit unreachable (FR-3.4).
     *
     * @param carried what the previous, contiguous window left open, so a session that outlives a
     *   poll keeps being measured. See [SpanFolder.fold].
     */
    fun read(fromMillis: Long, toMillis: Long, carried: OpenSpan?): ForegroundWindow?

    /** Why [read] is returning null, for a log line and for the on-device status screen. */
    fun unavailableReason(): String
}
