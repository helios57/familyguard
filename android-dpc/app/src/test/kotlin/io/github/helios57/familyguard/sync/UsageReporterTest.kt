package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.usage.ForegroundSpan
import io.github.helios57.familyguard.usage.InMemorySessionStore
import io.github.helios57.familyguard.usage.InMemoryUsageStore
import io.github.helios57.familyguard.usage.SessionLog
import io.github.helios57.familyguard.usage.UsageLedger
import io.github.helios57.familyguard.usage.UsageSession
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

/**
 * What survives a failed POST, and what survives the process dying.
 *
 * The loss this class exists to prevent has no symptom. A send that fails at 23:58 strands the whole
 * evening — the day never changes again, so nothing would ever mark it for re-sending, and the
 * minutes a child actually spent would simply not exist for the quota or the console.
 */
class UsageReporterTest {

    private val store = InMemoryUsageStore()
    private val ledger = UsageLedger(store)
    private val sends = mutableListOf<Pair<String, Map<String, Long>>>()
    private val sittings = mutableListOf<List<UsageSession>>()
    private val sessionStore = InMemorySessionStore()
    private val sessions = SessionLog(sessionStore)
    private var failWith: IOException? = null

    private fun reporter() = UsageReporter(ledger, sessions) { day, samples, attached ->
        failWith?.let { throw it }
        sends += day to samples
        sittings += attached
    }

    @Test
    fun `nothing outstanding sends nothing and is not a failure`() {
        val result = reporter().flush()

        assertTrue(result.ok)
        assertTrue(result.sent.isEmpty())
        assertTrue(sends.isEmpty())
    }

    @Test
    fun `a noted day is sent with the ledger's cumulative totals`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))

        val result = reporter.flush()

        assertTrue(result.ok)
        assertEquals(listOf(DAY), result.sent)
        assertEquals(listOf(DAY to mapOf(GAME to 20 * 60_000L)), sends)
    }

    @Test
    fun `a day the server accepted is not sent again`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))
        reporter.flush()

        val second = reporter.flush()

        assertTrue(second.sent.isEmpty())
        assertEquals(1, sends.size)
        assertTrue(reporter.outstanding().isEmpty())
    }

    /** The retry, and the reason the day stays pending rather than being dropped. */
    @Test
    fun `a failed send stays outstanding and is retried`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))

        failWith = IOException("no route to host")
        val failed = reporter.flush()

        assertFalse(failed.ok)
        assertEquals(mapOf(DAY to "no route to host"), failed.failed)
        assertEquals(setOf(DAY), reporter.outstanding())

        failWith = null
        val retried = reporter.flush()

        assertEquals(listOf(DAY), retried.sent)
        assertEquals(listOf(DAY to mapOf(GAME to 20 * 60_000L)), sends)
    }

    /** A retry carries what the ledger holds *now*, not the totals from the attempt that failed. */
    @Test
    fun `a retry sends the totals as they stand at the retry`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))
        failWith = IOException("no route to host")
        reporter.flush()

        measure(DAY, 5 * 60_000L)
        failWith = null
        reporter.flush()

        assertEquals(listOf(DAY to mapOf(GAME to 25 * 60_000L)), sends)
    }

    /**
     * The 23:58 case. Nothing else would ever mark that day again, so a reporter built after the
     * process restarted treats everything the ledger still holds as outstanding. Safe because the
     * totals are cumulative and the server upserts with `GREATEST`.
     */
    @Test
    fun `every day the ledger holds is outstanding when the reporter is created`() {
        measure("2026-08-16", 10 * 60_000L)
        measure("2026-08-17", 20 * 60_000L)

        val afterRestart =
            UsageReporter(UsageLedger(store), sessions) { day, samples, _ -> sends += day to samples }

        assertEquals(setOf("2026-08-16", "2026-08-17"), afterRestart.outstanding())
        assertTrue(afterRestart.flush().ok)
        assertEquals(listOf("2026-08-16", "2026-08-17"), sends.map { it.first })
    }

    @Test
    fun `one failing day does not stop the others`() {
        measure("2026-08-16", 10 * 60_000L)
        measure("2026-08-17", 20 * 60_000L)
        val reporter = UsageReporter(ledger, sessions) { day, samples, _ ->
            if (day == "2026-08-16") throw IOException("no route to host")
            sends += day to samples
        }
        reporter.note(listOf("2026-08-16", "2026-08-17"))

        val result = reporter.flush()

        assertEquals(listOf("2026-08-17"), result.sent)
        assertEquals(setOf("2026-08-16"), result.failed.keys)
        assertEquals(setOf("2026-08-16"), reporter.outstanding())
    }

    /** A day pruned out of the ledger has no total left to deliver, so it stops being outstanding. */
    @Test
    fun `a day with no totals is dropped rather than retried forever`() {
        val reporter = reporter()
        reporter.note(listOf("2026-01-01"))

        val result = reporter.flush()

        assertTrue(result.ok)
        assertTrue(result.sent.isEmpty())
        assertTrue(sends.isEmpty())
        assertTrue(reporter.outstanding().isEmpty())
    }

    @Test
    fun `noting the same day twice sends it once`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))
        reporter.note(listOf(DAY))

        reporter.flush()

        assertEquals(1, sends.size)
    }

    /**
     * An IOException with no message would otherwise report `null` in the log line the service
     * writes, which reads as "no reason" rather than as a class of failure.
     */
    @Test
    fun `a failure with no message is reported by its type`() {
        measure(DAY, 20 * 60_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))
        failWith = IOException()

        assertEquals(mapOf(DAY to "IOException"), reporter.flush().failed)
    }

    /** The summary line the service logs must not read as a success when a day failed. */
    @Test
    fun `the summary distinguishes a clean flush from a partial one`() {
        assertTrue(FlushResult(sent = listOf(DAY)).toString().contains("usage sent for 1 day"))
        assertTrue(FlushResult(failed = mapOf(DAY to "boom")).toString().contains("failed="))
    }

    // ---- the sittings that ride along with the day totals (FR-3.7) ----

    @Test
    fun `the sittings ride on the first day that lands, and only that one`() {
        measure("2026-08-16", 10 * 60_000L)
        measure("2026-08-17", 20 * 60_000L)
        record(GAME, 10_000L, 70_000L)
        val reporter = reporter()
        reporter.note(listOf("2026-08-16", "2026-08-17"))

        val result = reporter.flush()

        assertEquals(1, result.sessions)
        assertEquals(listOf(1, 0), sittings.map { it.size })
        assertEquals(0, sessions.pending())
    }

    /**
     * The loss this guards. The queue is emptied by the acknowledgement, not by the attempt — a
     * sitting is an event, so one that is thrown away before the server has it is simply gone.
     */
    @Test
    fun `a failed send keeps the sittings queued for the next flush`() {
        measure(DAY, 20 * 60_000L)
        record(GAME, 10_000L, 70_000L)
        val reporter = reporter()
        reporter.note(listOf(DAY))
        failWith = IOException("no route to host")

        val failed = reporter.flush()

        assertEquals(0, failed.sessions)
        assertEquals(1, sessions.pending())

        failWith = null
        val retried = reporter.flush()

        assertEquals(1, retried.sessions)
        assertEquals(listOf(1), sittings.map { it.size })
        assertEquals(0, sessions.pending())
    }

    /**
     * A phone that measured a sitting and owes the server no day total. The day totals are
     * cumulative and repair themselves; a sitting held back until the child next uses the phone
     * would be one a parent cannot see tonight.
     */
    @Test
    fun `sittings with no outstanding day are sent on their own`() {
        record(GAME, 10_000L, 70_000L)

        val result = reporter().flush()

        assertEquals(1, result.sessions)
        assertEquals(listOf("" to emptyMap<String, Long>()), sends)
        assertEquals(0, sessions.pending())
    }

    /** …and that send failing is a failure, not a quiet drop. */
    @Test
    fun `a failed sittings-only send is reported and keeps them queued`() {
        record(GAME, 10_000L, 70_000L)
        val reporter = reporter()
        failWith = IOException("no route to host")

        val result = reporter.flush()

        assertFalse(result.ok)
        assertEquals(mapOf("sessions" to "no route to host"), result.failed)
        assertEquals(1, sessions.pending())
    }

    /** A day whose totals were pruned still lets the sittings through. */
    @Test
    fun `sittings are delivered even when every outstanding day has been pruned`() {
        record(GAME, 10_000L, 70_000L)
        val reporter = reporter()
        reporter.note(listOf("2026-01-01"))

        val result = reporter.flush()

        assertTrue(result.ok)
        assertTrue(result.sent.isEmpty())
        assertEquals(1, result.sessions)
        assertEquals(listOf("" to emptyMap<String, Long>()), sends)
    }

    @Test
    fun `a flush with neither a day nor a sitting sends nothing`() {
        assertTrue(reporter().flush().ok)
        assertTrue(sends.isEmpty())
    }

    private fun record(pkg: String, fromMillis: Long, toMillis: Long) {
        sessions.record(listOf(ForegroundSpan(pkg, fromMillis, toMillis)), nowMillis = toMillis)
    }

    private fun measure(day: String, millis: Long) {
        ledger.add(mapOf(day to mapOf(GAME to millis)), budgetMillis = 24 * 60 * 60_000L)
    }

    private companion object {
        const val DAY = "2026-08-17"
        const val GAME = "com.example.game"
    }
}
