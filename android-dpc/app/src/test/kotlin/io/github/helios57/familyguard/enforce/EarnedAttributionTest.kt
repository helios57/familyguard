package io.github.helios57.familyguard.enforce

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-22: which of a measured window's minutes are earned time (Bonuszeit).
 *
 * A minute is earned time when it is a bonus app, inside bedtime, or past the daily budget — a
 * statement about WHEN it was used, which is why the phone decides it window by window rather than
 * from day totals. The case that forces it: a child who spends the budget on a game in the morning
 * and chats on WhatsApp in the evening must not pay gold for the chat.
 */
class EarnedAttributionTest {

    private val min = 60_000L
    private fun ctx(
        bonus: Set<String> = setOf(MOVIES),
        exempt: Set<String> = setOf(WHATSAPP, CAMERA),
        uncounted: Set<String> = setOf(LAUNCHER),
        inBedtime: Boolean = false,
        paused: Boolean = false,
        budgetLeftMs: Long = Long.MAX_VALUE,
    ) = EarnedContext(DAY, bonus, exempt, uncounted, inBedtime, paused, budgetLeftMs)

    @Test
    fun `a bonus app is earned time whole`() {
        assertEquals(mapOf(MOVIES to 7 * min), EarnedAttribution.attribute(mapOf(MOVIES to 7 * min), ctx()))
    }

    @Test
    fun `inside bedtime a governed app is earned time and a free one is not`() {
        val gold = EarnedAttribution.attribute(mapOf(GAME to 4 * min, WHATSAPP to 3 * min), ctx(inBedtime = true))
        assertEquals(mapOf(GAME to 4 * min), gold)
    }

    @Test
    fun `within the budget nothing is earned time`() {
        assertEquals(emptyMap<String, Long>(),
            EarnedAttribution.attribute(mapOf(GAME to 4 * min), ctx(budgetLeftMs = 10 * min)))
    }

    @Test
    fun `past the budget the overflow is earned time and lands on the governed apps`() {
        // 10 counted minutes against 5 left: 5 overflow, carried by the game, not by the camera.
        val gold = EarnedAttribution.attribute(mapOf(GAME to 8 * min, CAMERA to 2 * min), ctx(budgetLeftMs = 5 * min))
        assertEquals(mapOf(GAME to 5 * min), gold)
    }

    @Test
    fun `the overflow is shared among the governed apps in proportion`() {
        val gold = EarnedAttribution.attribute(mapOf(GAME to 6 * min, VIDEO to 4 * min), ctx(budgetLeftMs = 0))
        assertEquals(mapOf(GAME to 6 * min, VIDEO to 4 * min), gold)
    }

    @Test
    fun `chatting after the budget is gone costs no earned time`() {
        assertEquals(emptyMap<String, Long>(),
            EarnedAttribution.attribute(mapOf(WHATSAPP to 20 * min), ctx(budgetLeftMs = 0)))
    }

    @Test
    fun `uncounted time is neither budget nor earned time`() {
        assertEquals(emptyMap<String, Long>(),
            EarnedAttribution.attribute(mapOf(LAUNCHER to 30 * min), ctx(budgetLeftMs = 0, inBedtime = true)))
    }

    @Test
    fun `without a daily limit only bonus apps and bedtime cost earned time`() {
        val gold = EarnedAttribution.attribute(mapOf(GAME to 30 * min, MOVIES to 5 * min), ctx())
        assertEquals(mapOf(MOVIES to 5 * min), gold)
    }

    @Test
    fun `a pause costs nothing, because nothing that costs runs`() {
        assertEquals(emptyMap<String, Long>(),
            EarnedAttribution.attribute(mapOf(GAME to 3 * min, MOVIES to 3 * min), ctx(paused = true)))
    }

    private companion object {
        const val DAY = "2026-09-28"
        const val GAME = "com.example.game"
        const val VIDEO = "com.example.video"
        const val MOVIES = "com.example.movies"
        const val WHATSAPP = "com.whatsapp"
        const val CAMERA = "com.sec.android.app.camera"
        const val LAUNCHER = "com.sec.android.app.launcher"
    }
}
