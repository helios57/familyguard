package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.ApiException
import java.io.IOException
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** One recovery attempt, as it will be reported. */
@Serializable
data class RecoveryAttempt(
    @SerialName("succeeded") val succeeded: Boolean,
    @SerialName("occurred_at") val occurredAtEpochMillis: Long,
)

interface RecoveryJournalStore {
    fun load(): List<RecoveryAttempt>
    fun save(attempts: List<RecoveryAttempt>)
}

/** A [RecoveryJournalStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryRecoveryJournalStore(initial: List<RecoveryAttempt> = emptyList()) : RecoveryJournalStore {
    private var attempts: List<RecoveryAttempt> = initial
    override fun load(): List<RecoveryAttempt> = attempts
    override fun save(attempts: List<RecoveryAttempt>) {
        this.attempts = attempts
    }
}

/**
 * What one flush managed to deliver, what it will retry, and what it gave up on.
 *
 * [dropped] is separate from [failed] on purpose. Both are attempts the server did not record, and
 * folding them together would make "we will try again" and "this is gone" indistinguishable in the
 * one place that can still say so.
 */
data class RecoveryFlushResult(
    val sent: List<RecoveryAttempt> = emptyList(),
    val failed: Map<RecoveryAttempt, String> = emptyMap(),
    val dropped: Map<RecoveryAttempt, String> = emptyMap(),
) {
    val ok: Boolean get() = failed.isEmpty() && dropped.isEmpty()
}

/**
 * Every recovery attempt this device has made and not yet managed to report (FR-12.5).
 *
 * The attempts that matter most are the ones made with no network — that is the situation recovery
 * exists for — so "report it when it happens" is not an implementation of FR-12.5, it is the
 * implementation that reports nothing in the only case anyone cares about. They are written down
 * and carried until the server takes them.
 *
 * **Failures are kept, not only successes.** A run of failed attempts is the signal a parent needs:
 * a single failure is a mistype, and thirty is a child working through the last group of a code
 * they half-saw. Dropping them would leave the console showing one successful recovery and no way
 * to tell which of the two it was.
 *
 * **Persisted through the send, not before it.** An attempt is removed from the list only after the
 * server has accepted it. A device that dropped it on send would lose the record to any restart,
 * and a device that never dropped it would report the same attempt forever.
 *
 * **Bounded** (NFR-9). The lockout caps attempts at roughly one an hour once someone is guessing,
 * so [MAX_PENDING] is a backstop rather than a working limit — but a device offline for a month
 * with a determined child must not grow this file without limit. When it overflows, the *oldest*
 * entries go: the recent ones are the ones the parent can still act on, and the count of what was
 * dropped is not lost, because the total is what [outstanding] reports.
 */
class RecoveryJournal(
    private val store: RecoveryJournalStore,
    private val send: (RecoveryAttempt) -> Unit,
) {

    fun record(attempt: RecoveryAttempt) {
        val kept = (store.load() + attempt).takeLast(MAX_PENDING)
        store.save(kept)
    }

    fun outstanding(): List<RecoveryAttempt> = store.load()

    /**
     * Sends what is pending, oldest first, and keeps whatever was not accepted.
     *
     * Order matters here in a way it does not for usage totals: the console draws these as a
     * sequence, and a burst delivered newest-first would read as a successful recovery followed by
     * the failures that led to it. That ordering is also what makes the queue head-of-line blocked,
     * and [dropped] is the consequence — see below.
     *
     * A refusal stops the flush rather than working through the rest. They are going to the same
     * endpoint over the same connection, so the others would fail too, and each one costs a request
     * on a device that has just said it has no network.
     */
    fun flush(): RecoveryFlushResult {
        val pending = store.load()
        if (pending.isEmpty()) return RecoveryFlushResult()

        val sent = mutableListOf<RecoveryAttempt>()
        val dropped = LinkedHashMap<RecoveryAttempt, String>()
        val failed = LinkedHashMap<RecoveryAttempt, String>()
        var remaining = pending
        for (attempt in pending) {
            try {
                send(attempt)
                sent += attempt
            } catch (e: ApiException) {
                if (e.retryable) {
                    failed[attempt] = e.message ?: e.javaClass.simpleName
                    break
                }
                // The server will never accept this one. Keeping it would block every later attempt
                // behind it forever, which costs more records than the one being dropped — and a
                // refusal is itself reported, so it is not lost in silence.
                dropped[attempt] = e.message ?: e.javaClass.simpleName
            } catch (e: IOException) {
                failed[attempt] = e.message ?: e.javaClass.simpleName
                break
            } catch (e: RuntimeException) {
                failed[attempt] = e.message ?: e.javaClass.simpleName
                break
            }
            // Reached by a send that worked and by one the server refused permanently; both leave
            // the queue, and only the first is reported as sent.
            remaining = remaining - attempt
            store.save(remaining)
        }
        return RecoveryFlushResult(sent, failed, dropped)
    }

    companion object {
        /** See the class KDoc: a backstop against an unbounded file, not a working limit. */
        const val MAX_PENDING = 100
    }
}
