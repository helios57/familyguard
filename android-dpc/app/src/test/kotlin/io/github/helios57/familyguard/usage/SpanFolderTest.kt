package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Turning a real, untidy event stream into spans.
 *
 * Every case here is a shape the platform actually emits. The one that matters most is the missing
 * `PAUSED`: Android does not promise one before the next `RESUMED`, and a fold that waited for it
 * would leave a span open to the end of the window and credit an app the child closed hours ago —
 * arriving as usage, not as an error.
 *
 * **Two cases in this file used to assert the defect, and their names said so plainly** — *"a span
 * still open at the end of the window is closed there"* and *"no events at all is no usage, not an
 * open span"*. Both were true statements about the code and wrong statements about the platform:
 * an app that stays in the foreground emits no event, so closing at the window end threw the rest
 * of the session away. See [ContinuousForegroundTest] for what that measured.
 */
class SpanFolderTest {

    private val minute = 60_000L

    @Test
    fun `a resume followed by a pause is one span`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), paused(GAME, 6 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 6 * minute)), window.closed)
        assertNull("nothing is left in the foreground", window.open)
    }

    /** The lost-`PAUSED` case: the next resume closes whatever was open. */
    @Test
    fun `a resume closes the span that was open`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), resumed(CHAT, 4 * minute), paused(CHAT, 5 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(
            listOf(
                ForegroundSpan(GAME, 1 * minute, 4 * minute),
                ForegroundSpan(CHAT, 4 * minute, 5 * minute),
            ),
            window.closed,
        )
    }

    /**
     * Activities inside one app pause and resume around each other. Taking any pause as the end of
     * the session would cut a long session into a short one every time a second screen opened.
     */
    @Test
    fun `a pause for a package that is not the open one is ignored`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), paused(CHAT, 3 * minute), paused(GAME, 8 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 8 * minute)), window.closed)
    }

    /** FR-3.3: the span ends when the screen went off, not when the poll happened to run. */
    @Test
    fun `screen off closes the open span at the moment it went off`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), screenOff(3 * minute)),
            windowEndMillis = 8 * 60 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 3 * minute)), window.closed)
        assertNull("the screen going off ends the session, it does not carry it", window.open)
    }

    @Test
    fun `a screen off with nothing open produces nothing`() {
        val window = SpanFolder.fold(listOf(screenOff(3 * minute)), windowEndMillis = 10 * minute)

        assertTrue(window.closed.isEmpty())
        assertNull(window.open)
    }

    // ---- what outlives the window ---------------------------------------------------------------

    /**
     * A session still running when the poll fires is handed back, not closed.
     *
     * The platform reports transitions, so the next window will contain no `RESUMED` for this app
     * and can only know about it from here.
     */
    @Test
    fun `a span still open at the end of the window is handed back`() {
        val window = SpanFolder.fold(listOf(resumed(GAME, 1 * minute)), windowEndMillis = 10 * minute)

        assertTrue("nothing ended in this window", window.closed.isEmpty())
        assertEquals(OpenSpan(GAME, 1 * minute), window.open)
    }

    /** The other side of it: a window with no events at all continues what was already running. */
    @Test
    fun `a window with no events keeps the carried session open`() {
        val window = SpanFolder.fold(
            emptyList(),
            windowEndMillis = 10 * minute,
            carried = OpenSpan(GAME, 1 * minute),
        )

        assertTrue(window.closed.isEmpty())
        assertEquals("still open, and still since the moment it was opened", OpenSpan(GAME, 1 * minute), window.open)
    }

    /** A carried session that ends in this window reports its TRUE start, not the poll boundary. */
    @Test
    fun `a carried session that ends here keeps the start it really had`() {
        val window = SpanFolder.fold(
            listOf(paused(GAME, 12 * minute)),
            windowEndMillis = 15 * minute,
            carried = OpenSpan(GAME, 1 * minute),
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 12 * minute)), window.closed)
        assertNull(window.open)
    }

    /** A carried session that is replaced by another app is closed at the handover. */
    @Test
    fun `a carried session is closed by a resume of a different app`() {
        val window = SpanFolder.fold(
            listOf(resumed(CHAT, 12 * minute)),
            windowEndMillis = 15 * minute,
            carried = OpenSpan(GAME, 1 * minute),
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 12 * minute)), window.closed)
        assertEquals(OpenSpan(CHAT, 12 * minute), window.open)
    }

    @Test
    fun `no events and nothing carried is no usage`() {
        val window = SpanFolder.fold(emptyList(), windowEndMillis = 10 * minute)

        assertTrue(window.closed.isEmpty())
        assertNull(window.open)
    }

    // ---- the untidy shapes ----------------------------------------------------------------------

    @Test
    fun `an app resumed after the window end is still open, not discarded`() {
        val window = SpanFolder.fold(listOf(resumed(GAME, 20 * minute)), windowEndMillis = 10 * minute)

        assertTrue(window.closed.isEmpty())
        assertEquals(OpenSpan(GAME, 20 * minute), window.open)
    }

    /**
     * `queryEvents` returns in timestamp order today. A fold that trusted that and silently produced
     * negative-length spans if it ever did not would show up as usage quietly going missing.
     */
    @Test
    fun `events out of order are folded as if they were in order`() {
        val ordered = listOf(resumed(GAME, 1 * minute), paused(GAME, 6 * minute))

        assertEquals(
            SpanFolder.fold(ordered, windowEndMillis = 10 * minute),
            SpanFolder.fold(ordered.reversed(), windowEndMillis = 10 * minute),
        )
    }

    @Test
    fun `a zero-length span is not emitted`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 5 * minute), paused(GAME, 5 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertTrue(window.closed.isEmpty())
        assertNull(window.open)
    }

    @Test
    fun `a resume with no package name is ignored and leaves the open span alone`() {
        val window = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), resumed("", 4 * minute), paused(GAME, 8 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 8 * minute)), window.closed)
    }

    private fun resumed(pkg: String, at: Long) = ForegroundEvent(ForegroundEventKind.RESUMED, pkg, at)
    private fun paused(pkg: String, at: Long) = ForegroundEvent(ForegroundEventKind.PAUSED, pkg, at)
    private fun screenOff(at: Long) = ForegroundEvent(ForegroundEventKind.SCREEN_OFF, "", at)

    private companion object {
        const val GAME = "com.example.game"
        const val CHAT = "com.example.chat"
    }
}
