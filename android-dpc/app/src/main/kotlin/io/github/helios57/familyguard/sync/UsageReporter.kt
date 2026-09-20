package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.usage.SessionLog
import io.github.helios57.familyguard.usage.UsageLedger
import io.github.helios57.familyguard.usage.UsageSession
import java.io.IOException

/** What one flush of the outstanding day totals did. */
data class FlushResult(
    val sent: List<String> = emptyList(),
    val failed: Map<String, String> = emptyMap(),
    /** How many sittings the server acknowledged, and the phone therefore stopped holding. */
    val sessions: Int = 0,
) {
    val ok: Boolean get() = failed.isEmpty()

    override fun toString(): String = buildString {
        if (ok) append("usage sent for ${sent.size} day(s)") else append("usage sent=${sent.size} failed=$failed")
        if (sessions > 0) append(", $sessions session(s)")
    }
}

/**
 * Sends the day totals the server has not accepted yet, and keeps sending until it does.
 *
 * **Every day the ledger still holds is outstanding when this object is created.** That is the
 * cheapest correct answer to two different losses at once. A POST that fails at 23:58 would
 * otherwise strand the evening: the day never changes again, so nothing would ever re-send it, and
 * the minutes a child actually spent would simply not exist. A process killed between measuring and
 * sending loses the same way. Re-sending on startup covers both, and costs at most seven small
 * requests because the ledger keeps a week.
 *
 * Re-sending is safe because the totals are cumulative and the server upserts with
 * `GREATEST(stored, reported)` — a day sent twice cannot double-count, and one sent out of order
 * cannot roll a total backwards.
 */
class UsageReporter(
    private val ledger: UsageLedger,
    /**
     * The undelivered sittings (FR-3.7), which ride along with whichever day goes first.
     *
     * Attached rather than sent separately so the two facts measured by one poll cost one request.
     * They are acknowledged only on a send that returned, and only the exact sessions that were
     * attached — [SessionLog.acknowledge] takes the list, not a count, because the poll that
     * measures can run between the send and this line.
     */
    private val sessions: SessionLog,
    private val send: (day: String, samples: Map<String, Long>, sittings: List<UsageSession>) -> Unit,
) {

    private val pending: MutableSet<String> = LinkedHashSet(ledger.days())

    /** Records that these days have new totals to deliver. */
    fun note(days: Collection<String>) {
        pending += days
    }

    /** Days that are measured and not yet acknowledged by the server. */
    fun outstanding(): Set<String> = pending.toSet()

    fun flush(): FlushResult {
        // Read once, before anything is sent: whatever is attached must be exactly what is
        // acknowledged, and re-reading the queue after a successful send would acknowledge sittings
        // the server has not seen.
        var carry = sessions.batch(MAX_SESSIONS_PER_REPORT)
        if (pending.isEmpty() && carry.isEmpty()) return FlushResult()
        val sent = mutableListOf<String>()
        val failed = linkedMapOf<String, String>()
        var delivered = 0
        // A copy: a send that succeeds removes its day, and mutating the set being iterated is a
        // ConcurrentModificationException on the happy path.
        for (day in pending.toList()) {
            val samples = ledger.totals(day)
            if (samples.isEmpty()) {
                // Nothing measured for a day that was pruned out from under us. Dropping it is not a
                // loss: there is no total left to deliver.
                pending -= day
                continue
            }
            try {
                send(day, samples, carry)
                pending -= day
                sent += day
                if (carry.isNotEmpty()) {
                    sessions.acknowledge(carry)
                    delivered += carry.size
                    // One batch per flush, on the first day that lands. The rest of the days carry
                    // nothing, which is what keeps a three-day catch-up from sending the same
                    // sittings three times.
                    carry = emptyList()
                }
            } catch (e: IOException) {
                // Kept pending on purpose. The next flush retries it, and the one after that.
                failed[day] = e.message ?: e::class.java.simpleName
            }
        }
        if (carry.isNotEmpty()) {
            // Sittings with no day to ride on: every day was already acknowledged, or the only ones
            // left failed. A report with no samples is a real report — the server dates it itself
            // and `RecordUsage` over an empty map changes no total — so this delivers them rather
            // than holding them until the child next uses the phone.
            try {
                send("", emptyMap(), carry)
                sessions.acknowledge(carry)
                delivered += carry.size
            } catch (e: IOException) {
                // Not keyed by a day, because it is not about one. The queue keeps them.
                failed["sessions"] = e.message ?: e::class.java.simpleName
            }
        }
        return FlushResult(sent, failed, delivered)
    }

    companion object {
        /**
         * The server refuses more than this in one report, so sending more would be sending
         * something that is silently discarded. It matches `maxUsageSessionsPerReport`.
         */
        const val MAX_SESSIONS_PER_REPORT = 1_000
    }
}
