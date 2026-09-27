package io.github.helios57.familyguard.usage

import io.github.helios57.familyguard.enforce.EarnedContext
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-22 on the phone: what a window charges, and how today's count splits into budget minutes and
 * earned minutes — the two numbers the engine takes.
 */
class EarnedAccountTest {

    private val min = 60_000L
    private val day = "2026-09-28"
    private val account = EarnedAccount(UsageLedger(InMemoryUsageStore()))
    private fun ctx(budgetLeftMs: Long = 0, inBedtime: Boolean = false) = EarnedContext(
        day = day, bonus = setOf(MOVIES), exempt = setOf(CHAT), uncounted = setOf(HOME),
        inBedtime = inBedtime, paused = false, budgetLeftMs = budgetLeftMs,
    )

    @Test
    fun `nothing is charged before the engine has run once`() {
        account.charge(mapOf(day to mapOf(MOVIES to 5 * min)))
        assertEquals(emptyMap<String, Long>(), account.ledger.totals(day))
    }

    @Test
    fun `windows accumulate, and today's count splits into budget and earned minutes`() {
        account.context = ctx(budgetLeftMs = 0)
        account.charge(mapOf(day to mapOf(GAME to 10 * min, CHAT to 5 * min, MOVIES to 7 * min)))
        account.charge(mapOf(day to mapOf(MOVIES to 3 * min)))
        assertEquals(mapOf(GAME to 10 * min, MOVIES to 10 * min), account.ledger.totals(day))

        val counted = mapOf(GAME to 10 * min, CHAT to 5 * min, MOVIES to 10 * min)
        val (budget, earned) = account.split(day, counted, setOf(HOME))
        assertEquals("the chat is budget, the game and the film are earned", 5, budget)
        assertEquals(20, earned)
    }

    @Test
    fun `a window on another day charges only bonus apps`() {
        account.context = ctx(budgetLeftMs = 0, inBedtime = true)
        account.charge(mapOf("2026-09-27" to mapOf(GAME to 4 * min, MOVIES to 2 * min)))
        assertEquals(mapOf(MOVIES to 2 * min), account.ledger.totals("2026-09-27"))
    }

    @Test
    fun `the ledger returns what it credited, after the budget ceiling`() {
        val ledger = UsageLedger(InMemoryUsageStore())
        // 20 minutes measured against 10 minutes of screen-on time: each package is scaled by half.
        val credited = ledger.addCredited(mapOf(day to mapOf(GAME to 12 * min, CHAT to 8 * min)), 10 * min)
        assertEquals(mapOf(day to mapOf(GAME to 6 * min, CHAT to 4 * min)), credited)
    }

    private companion object {
        const val GAME = "com.example.game"
        const val MOVIES = "com.example.movies"
        const val CHAT = "com.whatsapp"
        const val HOME = "com.sec.android.app.launcher"
    }
}
