package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The running totals, the monotonic budget, and what survives a process death.
 *
 * Two failures are being defended against, and both are silent. Sending deltas through the server's
 * `GREATEST` upsert discards every duplicate and keeps every gap; forgetting the totals on a reboot
 * makes the evening invisible until the fresh counter overtakes the morning's. Neither produces an
 * error — they produce a child who appears to have been off their phone.
 */
class UsageLedgerTest {

    private val store = InMemoryUsageStore()
    private val ledger = UsageLedger(store)

    private val hour = 60 * 60_000L

    @Test
    fun `totals accumulate across windows rather than replacing`() {
        ledger.add(mapOf(DAY to mapOf(GAME to 10 * 60_000L)), budgetMillis = hour)
        ledger.add(mapOf(DAY to mapOf(GAME to 5 * 60_000L)), budgetMillis = hour)

        assertEquals(15 * 60_000L, ledger.totals(DAY).getValue(GAME))
    }

    @Test
    fun `the days that changed are reported, and only those`() {
        ledger.add(mapOf("2026-08-16" to mapOf(GAME to 60_000L)), budgetMillis = hour)

        val changed = ledger.add(
            mapOf(
                "2026-08-17" to mapOf(GAME to 60_000L),
                "2026-08-18" to mapOf(CHAT to 60_000L),
            ),
            budgetMillis = hour,
        )

        assertEquals(setOf("2026-08-17", "2026-08-18"), changed)
    }

    // ---- the budget (FR-3.2, FR-3.3) -----------------------------------------------------------

    /**
     * A wall clock pushed forward makes the platform's own timestamps span more time than really
     * elapsed. Two hours of reported foreground time inside a window the monotonic clock says was ten
     * minutes long is credited as ten minutes.
     */
    @Test
    fun `no more time is credited than the monotonic clock allows`() {
        ledger.add(mapOf(DAY to mapOf(GAME to 2 * hour)), budgetMillis = 10 * 60_000L)

        assertEquals(10 * 60_000L, ledger.totals(DAY).getValue(GAME))
    }

    /** Which package loses the excess must not depend on map iteration order. */
    @Test
    fun `an over-budget window is scaled down in proportion, not truncated`() {
        ledger.add(
            mapOf(DAY to mapOf(GAME to 3 * hour, CHAT to 1 * hour)),
            budgetMillis = hour,
        )

        val totals = ledger.totals(DAY)
        assertEquals(45 * 60_000L, totals.getValue(GAME))
        assertEquals(15 * 60_000L, totals.getValue(CHAT))
    }

    /** A window measured under its budget is credited as measured, not stretched up to it. */
    @Test
    fun `an under-budget window is credited unchanged`() {
        ledger.add(mapOf(DAY to mapOf(GAME to 5 * 60_000L)), budgetMillis = hour)

        assertEquals(5 * 60_000L, ledger.totals(DAY).getValue(GAME))
    }

    /**
     * FR-3.3. Time with the screen off is not in the budget at all, so nothing the platform reports
     * about that window can be credited — including a reader that answers with a stale span.
     */
    @Test
    fun `a window with no screen-on budget credits nothing`() {
        val changed = ledger.add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = 0)

        assertTrue(changed.isEmpty())
        assertTrue(ledger.totals(DAY).isEmpty())
    }

    @Test
    fun `a negative budget credits nothing`() {
        ledger.add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = -1)

        assertTrue(ledger.totals(DAY).isEmpty())
    }

    // ---- persistence ---------------------------------------------------------------------------

    /**
     * The reboot case. A ledger built on a store that already holds a morning continues from it, so
     * what is reported in the evening is a total the server's `GREATEST` will accept.
     */
    @Test
    fun `a ledger resumes from what was stored`() {
        ledger.add(mapOf(DAY to mapOf(GAME to 30 * 60_000L)), budgetMillis = hour)

        val afterReboot = UsageLedger(store)
        afterReboot.add(mapOf(DAY to mapOf(GAME to 10 * 60_000L)), budgetMillis = hour)

        assertEquals(40 * 60_000L, afterReboot.totals(DAY).getValue(GAME))
    }

    @Test
    fun `nothing is written for a window that credited nothing`() {
        val counting = CountingStore()

        UsageLedger(counting).add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = 0)

        assertEquals(0, counting.saves)
    }

    @Test
    fun `clearing forgets the stored totals too`() {
        ledger.add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = hour)

        ledger.clear()

        assertTrue(ledger.totals(DAY).isEmpty())
        assertTrue("a new ledger read back what was cleared", UsageLedger(store).days().isEmpty())
    }

    /**
     * The server refuses a day older than a week, so history beyond that can never be delivered — and
     * a map that only grows is unbounded state on a phone that may not reach the network for months.
     */
    @Test
    fun `only the most recent days are retained`() {
        val short = UsageLedger(store, retainDays = 3)
        for (day in 10..15) {
            short.add(mapOf("2026-08-$day" to mapOf(GAME to 60_000L)), budgetMillis = hour)
        }

        assertEquals(listOf("2026-08-13", "2026-08-14", "2026-08-15"), short.days())
        assertTrue("a pruned day is gone from storage too", UsageLedger(store).days().size == 3)
    }

    // ---- rubbish in ----------------------------------------------------------------------------

    @Test
    fun `blank packages and non-positive durations are dropped`() {
        val changed = ledger.add(
            mapOf(DAY to mapOf("" to hour, GAME to 0L, CHAT to -5L)),
            budgetMillis = hour,
        )

        assertTrue(changed.isEmpty())
        assertTrue(ledger.totals(DAY).isEmpty())
    }

    @Test
    fun `a day never seen has no totals rather than throwing`() {
        assertTrue(ledger.totals("2026-01-01").isEmpty())
    }

    /** The map handed back is a copy: a caller that mutates it must not change the ledger. */
    @Test
    fun `the totals handed out are a snapshot`() {
        ledger.add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = hour)

        val first = ledger.totals(DAY)
        ledger.add(mapOf(DAY to mapOf(GAME to hour)), budgetMillis = hour)

        assertEquals(hour, first.getValue(GAME))
        assertEquals(2 * hour, ledger.totals(DAY).getValue(GAME))
    }

    private class CountingStore : UsageStore {
        var saves = 0
        private var state: Map<String, Map<String, Long>> = emptyMap()
        override fun load(): Map<String, Map<String, Long>> = state
        override fun save(totals: Map<String, Map<String, Long>>) {
            saves++
            state = totals
        }
    }

    private companion object {
        const val DAY = "2026-08-17"
        const val GAME = "com.example.game"
        const val CHAT = "com.example.chat"
    }
}
