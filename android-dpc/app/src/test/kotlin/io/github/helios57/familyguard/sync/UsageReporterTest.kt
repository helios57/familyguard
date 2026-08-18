package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.usage.InMemoryUsageStore
import io.github.helios57.familyguard.usage.UsageLedger
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
    private var failWith: IOException? = null

    private fun reporter() = UsageReporter(ledger) { day, samples ->
        failWith?.let { throw it }
        sends += day to samples
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

        val afterRestart = UsageReporter(UsageLedger(store)) { day, samples -> sends += day to samples }

        assertEquals(setOf("2026-08-16", "2026-08-17"), afterRestart.outstanding())
        assertTrue(afterRestart.flush().ok)
        assertEquals(listOf("2026-08-16", "2026-08-17"), sends.map { it.first })
    }

    @Test
    fun `one failing day does not stop the others`() {
        measure("2026-08-16", 10 * 60_000L)
        measure("2026-08-17", 20 * 60_000L)
        val reporter = UsageReporter(ledger) { day, samples ->
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

    private fun measure(day: String, millis: Long) {
        ledger.add(mapOf(day to mapOf(GAME to millis)), budgetMillis = 24 * 60 * 60_000L)
    }

    private companion object {
        const val DAY = "2026-08-17"
        const val GAME = "com.example.game"
    }
}
