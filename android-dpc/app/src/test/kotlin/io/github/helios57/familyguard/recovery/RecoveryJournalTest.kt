package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.ApiException
import java.io.IOException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The queue of recovery attempts a device has made and not yet managed to report (FR-12.5).
 *
 * The attempts that matter most are made with no network — that is the situation recovery exists for
 * — so the only interesting cases here are the ones where the send fails. A journal tested only
 * against a server that answers is a journal tested in the one condition it was not written for.
 *
 * Three distinctions this suite exists to keep apart, because collapsing any of them looks like
 * working software:
 *
 * - **sent** and **still queued**: an attempt removed before the server accepted it is an attempt
 *   nobody will ever hear about;
 * - **failed** and **dropped**: "we will try again" and "this is gone" must not read the same on the
 *   console;
 * - **failures** and **successes**: a run of thirty failures is a child working through the last
 *   group of a code they half-saw, and a journal that kept only successes would show one recovery
 *   and no way to tell the two apart.
 */
class RecoveryJournalTest {

    private val store = InMemoryRecoveryJournalStore()
    private val sent = mutableListOf<RecoveryAttempt>()

    /** Sends everything, and records the order it was asked in. */
    private fun accepting() = RecoveryJournal(store) { sent += it }

    private fun attempt(at: Long, succeeded: Boolean = false) =
        RecoveryAttempt(succeeded = succeeded, occurredAtEpochMillis = at)

    // ---- what is written down ----------------------------------------------------------------

    @Test
    fun `failures are kept, not only the success that ended them`() {
        val journal = accepting()
        journal.record(attempt(1, succeeded = false))
        journal.record(attempt(2, succeeded = false))
        journal.record(attempt(3, succeeded = true))

        assertEquals(
            listOf(attempt(1), attempt(2), attempt(3, succeeded = true)),
            journal.outstanding(),
        )
    }

    /**
     * NFR-9: a device offline for a month with a determined child must not grow this file without
     * limit.
     *
     * The *oldest* go, which is the half worth pinning. Dropping the newest would leave the console
     * showing the beginning of a guessing run and nothing since, and the recent entries are the ones
     * a parent can still act on.
     */
    @Test
    fun `an overflowing journal drops its oldest entries, not its newest`() {
        val journal = accepting()
        repeat(RecoveryJournal.MAX_PENDING + 5) { journal.record(attempt(it.toLong())) }

        val kept = journal.outstanding()

        assertEquals(RecoveryJournal.MAX_PENDING, kept.size)
        assertEquals(attempt(5), kept.first())
        assertEquals(attempt((RecoveryJournal.MAX_PENDING + 4).toLong()), kept.last())
    }

    /** The queue survives the object. A journal held in a field is a journal lost to every restart. */
    @Test
    fun `a restart finds the queue where it was left`() {
        accepting().record(attempt(1))

        assertEquals(listOf(attempt(1)), RecoveryJournal(store) { }.outstanding())
    }

    // ---- a flush that works ------------------------------------------------------------------

    @Test
    fun `a flush with nothing pending sends nothing and reports ok`() {
        val result = accepting().flush()

        assertTrue(result.ok)
        assertEquals(emptyList<RecoveryAttempt>(), result.sent)
        assertEquals("an empty queue produced a request", 0, sent.size)
    }

    /**
     * Oldest first. The console draws these as a sequence, and a burst delivered newest-first would
     * read as a successful recovery followed by the failures that led to it — the opposite of what
     * happened, and the opposite of what a parent would act on.
     */
    @Test
    fun `a flush sends oldest first and empties the queue`() {
        val journal = accepting()
        journal.record(attempt(1))
        journal.record(attempt(2))
        journal.record(attempt(3, succeeded = true))

        val result = journal.flush()

        assertTrue("a clean flush reported problems: $result", result.ok)
        assertEquals(listOf(attempt(1), attempt(2), attempt(3, succeeded = true)), sent)
        assertEquals(sent, result.sent)
        assertEquals(emptyList<RecoveryAttempt>(), journal.outstanding())
    }

    // ---- a flush that cannot get through -----------------------------------------------------

    /**
     * The head-of-line case, and the one that decides whether a device reports anything at all after
     * a bad night.
     *
     * The first attempt is delivered and *removed*; the second is refused with something worth
     * retrying and stays, with everything behind it. An implementation that saved only at the end
     * would re-send the first one forever; one that saved before sending would lose it.
     */
    @Test
    fun `a retryable refusal stops the flush and keeps everything from there on`() {
        val journal = RecoveryJournal(store) { attempt ->
            if (attempt.occurredAtEpochMillis == 2L) throw serverError() else sent += attempt
        }
        journal.record(attempt(1))
        journal.record(attempt(2))
        journal.record(attempt(3))

        val result = journal.flush()

        assertFalse(result.ok)
        assertEquals(listOf(attempt(1)), result.sent)
        assertEquals(setOf(attempt(2)), result.failed.keys)
        assertEquals("the flush carried on past a refusal", listOf(attempt(1)), sent)
        assertEquals(
            "the delivered attempt was re-queued, or the refused one was lost",
            listOf(attempt(2), attempt(3)),
            journal.outstanding(),
        )
    }

    /** No network at all — the case this whole class exists for. Nothing is lost. */
    @Test
    fun `a device with no network keeps everything it has not reported`() {
        val journal = RecoveryJournal(store) { throw IOException("network is unreachable") }
        journal.record(attempt(1))
        journal.record(attempt(2))

        val result = journal.flush()

        assertFalse(result.ok)
        assertEquals(setOf(attempt(1)), result.failed.keys)
        assertEquals(listOf(attempt(1), attempt(2)), journal.outstanding())
    }

    /**
     * A bug in the send path is a network failure as far as the queue is concerned.
     *
     * Not because they are the same thing, but because the alternative is an exception escaping
     * `flush` into whichever background job called it — and a crash there loses the queue's owner,
     * not just the request.
     */
    @Test
    fun `an unexpected exception stops the flush without escaping it`() {
        val journal = RecoveryJournal(store) { throw IllegalStateException("no credential to report with") }
        journal.record(attempt(1))

        val result = journal.flush()

        assertFalse(result.ok)
        assertEquals(setOf(attempt(1)), result.failed.keys)
        assertEquals(listOf(attempt(1)), journal.outstanding())
    }

    /**
     * A permanent refusal drops that one attempt and carries on.
     *
     * The alternative is worse than losing it: an attempt the server will never accept sits at the
     * head of the queue forever and blocks every later one, so one unreportable event costs every
     * event after it. The drop is reported rather than silent, which is the only reason it is
     * acceptable at all.
     */
    @Test
    fun `a permanent refusal drops that attempt and keeps going`() {
        val journal = RecoveryJournal(store) { attempt ->
            if (attempt.occurredAtEpochMillis == 1L) throw rejected() else sent += attempt
        }
        journal.record(attempt(1))
        journal.record(attempt(2))

        val result = journal.flush()

        assertFalse("a dropped attempt was reported as a clean flush", result.ok)
        assertEquals(setOf(attempt(1)), result.dropped.keys)
        assertEquals("a dropped attempt was reported as sent", listOf(attempt(2)), result.sent)
        assertEquals(emptyList<RecoveryAttempt>(), journal.outstanding())
        assertTrue("the drop was reported with no reason", result.dropped.values.all { it.isNotEmpty() })
    }

    /**
     * Two attempts the server will never accept do not stop each other either.
     *
     * The single-drop case above is passed by an implementation that breaks out of the loop after
     * the first drop, which would leave a device that hit two of them stuck behind the second.
     */
    @Test
    fun `consecutive permanent refusals are all dropped`() {
        val journal = RecoveryJournal(store) { throw rejected() }
        journal.record(attempt(1))
        journal.record(attempt(2))

        val result = journal.flush()

        assertEquals(setOf(attempt(1), attempt(2)), result.dropped.keys)
        assertEquals(emptyList<RecoveryAttempt>(), journal.outstanding())
    }

    /**
     * `failed` and `dropped` are separate fields and must stay separate.
     *
     * Folded together they would still make `ok` false, so every assertion about success keeps
     * passing while the console loses the only distinction that tells a parent whether the missing
     * event is coming.
     */
    @Test
    fun `an attempt that will be retried is never reported as one that was given up on`() {
        val journal = RecoveryJournal(store) { attempt ->
            if (attempt.occurredAtEpochMillis == 1L) throw rejected() else throw serverError()
        }
        journal.record(attempt(1))
        journal.record(attempt(2))

        val result = journal.flush()

        assertEquals(setOf(attempt(1)), result.dropped.keys)
        assertEquals(setOf(attempt(2)), result.failed.keys)
        assertEquals("the one that will be retried was dropped from the queue", listOf(attempt(2)), journal.outstanding())
    }

    private companion object {
        /** 503: worth another attempt later. `retryable` is the property under test, not the code. */
        fun serverError() = ApiException(503, "unavailable", "the backend is restarting", "req-1")

        /** 422: the server will never accept this one, however many times it is offered. */
        fun rejected() = ApiException(422, "invalid", "occurred_at is not a timestamp", "req-2")
    }
}
