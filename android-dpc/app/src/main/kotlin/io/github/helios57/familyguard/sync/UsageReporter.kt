package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.usage.UsageLedger
import java.io.IOException

/** What one flush of the outstanding day totals did. */
data class FlushResult(
    val sent: List<String> = emptyList(),
    val failed: Map<String, String> = emptyMap(),
) {
    val ok: Boolean get() = failed.isEmpty()

    override fun toString(): String =
        if (ok) "usage sent for ${sent.size} day(s)" else "usage sent=${sent.size} failed=$failed"
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
    private val send: (day: String, samples: Map<String, Long>) -> Unit,
) {

    private val pending: MutableSet<String> = LinkedHashSet(ledger.days())

    /** Records that these days have new totals to deliver. */
    fun note(days: Collection<String>) {
        pending += days
    }

    /** Days that are measured and not yet acknowledged by the server. */
    fun outstanding(): Set<String> = pending.toSet()

    fun flush(): FlushResult {
        if (pending.isEmpty()) return FlushResult()
        val sent = mutableListOf<String>()
        val failed = linkedMapOf<String, String>()
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
                send(day, samples)
                pending -= day
                sent += day
            } catch (e: IOException) {
                // Kept pending on purpose. The next flush retries it, and the one after that.
                failed[day] = e.message ?: e::class.java.simpleName
            }
        }
        return FlushResult(sent, failed)
    }
}
