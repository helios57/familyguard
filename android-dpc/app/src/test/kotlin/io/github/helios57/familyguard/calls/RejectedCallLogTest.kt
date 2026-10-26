package io.github.helios57.familyguard.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-31.5: the rejected calls waiting to reach the server. Like a sitting, a rejected call is an
 * event: re-sent until the server has it, never dropped before, and identified by an id the server
 * deduplicates on, so a lost response costs a re-send and never a second row.
 */
class RejectedCallLogTest {

    private class Memory : RejectedCallStore {
        var calls: List<RejectedCall> = emptyList()
        override fun load() = calls
        override fun save(calls: List<RejectedCall>) { this.calls = calls }
    }

    private var next = 0
    private val store = Memory()
    private val log = RejectedCallLog(store) { "id-${++next}" }

    @Test
    fun `a rejected call waits with its number, time and an id of its own`() {
        log.add("+15550199", 1_000L)
        log.add("+15550188", 2_000L)
        assertEquals(listOf(RejectedCall("id-1", "+15550199", 1_000L), RejectedCall("id-2", "+15550188", 2_000L)), log.pending())
    }

    @Test
    fun `only what the server acknowledged leaves the queue`() {
        log.add("+15550199", 1_000L)
        log.add("+15550188", 2_000L)
        val sent = log.pending()
        log.add("+15550177", 3_000L) // arrives while the upload is in flight
        log.acknowledge(sent.map { it.id })
        assertEquals(listOf(RejectedCall("id-3", "+15550177", 3_000L)), log.pending())
    }

    @Test
    fun `a re-send carries the same ids`() {
        log.add("+15550199", 1_000L)
        val first = log.pending()
        // The response was lost: nothing was acknowledged, and the next attempt is the same call.
        assertEquals(first, RejectedCallLog(store) { "other" }.pending())
    }

    @Test
    fun `the queue is bounded and keeps the newest`() {
        repeat(RejectedCallLog.MAX + 5) { log.add("+1555${it}", it.toLong()) }
        val pending = log.pending()
        assertEquals(RejectedCallLog.MAX, pending.size)
        assertTrue(pending.first().atMillis == 5L && pending.last().atMillis == (RejectedCallLog.MAX + 4).toLong())
    }
}
