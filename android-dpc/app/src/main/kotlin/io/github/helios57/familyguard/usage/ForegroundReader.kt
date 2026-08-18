package io.github.helios57.familyguard.usage

/**
 * Where the foreground spans for a window come from.
 *
 * `null` from [spans] means **not measured**, and that is a different answer from an empty list.
 * Empty says the screen was on and nothing ran; null says this device cannot see what ran at all.
 * Collapsing the two is the failure this interface exists to prevent: usage that reads zero makes
 * every daily quota unreachable, so the child gets unlimited screen time and the console shows a
 * healthy device with a well-behaved child (FR-3.4).
 */
interface ForegroundReader {

    /** @return the spans the platform reports for `[fromMillis, toMillis)`, or null if not measured. */
    fun spans(fromMillis: Long, toMillis: Long): List<ForegroundSpan>?

    /** Why [spans] is returning null, for a log line and for the on-device status screen. */
    fun unavailableReason(): String
}
