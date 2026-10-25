package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-30.3 on the phone: which credited windows count as learning, and how the day's learning turns
 * into the minutes the engine adds to the balance.
 */
class LearningAccountTest {

    private val sec = 1_000L
    private val min = 60_000L
    private val day = "2026-10-25"
    private val account = LearningAccount(UsageLedger(InMemoryUsageStore()))
    private fun ctx(enforcing: Boolean = true) = LearningContext(day = day, learning = setOf(LETTERS, NUMBERS), enforcing = enforcing)

    @Test
    fun `credits only learning packages`() {
        account.credit(mapOf(day to mapOf(LETTERS to 3 * min, MOVIES to 10 * min)))
        assertEquals("nothing before the engine has run once", 0, account.minutesToday(day))
        account.context = ctx()
        account.credit(mapOf(day to mapOf(LETTERS to 3 * min, MOVIES to 10 * min, NUMBERS to 2 * min)))
        assertEquals(mapOf(LETTERS to 3 * min, NUMBERS to 2 * min), account.ledger.totals(day))
        assertEquals(5, account.minutesToday(day))
    }

    @Test
    fun `nothing while paused or watch-only`() {
        account.context = ctx(enforcing = false)
        account.credit(mapOf(day to mapOf(LETTERS to 30 * min)))
        assertEquals(0, account.minutesToday(day))
    }

    @Test
    fun `floors the day's sum, not each app`() {
        account.context = ctx()
        account.credit(mapOf(day to mapOf(LETTERS to 40 * sec, NUMBERS to 40 * sec)))
        account.credit(mapOf(day to mapOf(LETTERS to 40 * sec)))
        assertEquals("3 x 40 s is 2 minutes, though no app reached one on its own", 2, account.minutesToday(day))
    }

    @Test
    fun `a window on another day credits nothing`() {
        account.context = ctx()
        account.credit(mapOf("2026-10-24" to mapOf(LETTERS to 5 * min)))
        assertEquals(emptyMap<String, Long>(), account.ledger.totals("2026-10-24"))
    }

    private companion object {
        const val LETTERS = "org.example.learn.letters"
        const val NUMBERS = "org.example.learn.numbers"
        const val MOVIES = "com.example.movies"
    }
}
