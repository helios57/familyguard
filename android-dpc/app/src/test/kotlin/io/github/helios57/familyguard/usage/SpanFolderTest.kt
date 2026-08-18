package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Turning a real, untidy event stream into spans.
 *
 * Every case here is a shape the platform actually emits. The one that matters most is the missing
 * `PAUSED`: Android does not promise one before the next `RESUMED`, and a fold that waited for it
 * would leave a span open to the end of the window and credit an app the child closed hours ago —
 * arriving as usage, not as an error.
 */
class SpanFolderTest {

    private val minute = 60_000L

    @Test
    fun `a resume followed by a pause is one span`() {
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), paused(GAME, 6 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 6 * minute)), spans)
    }

    /** The lost-`PAUSED` case: the next resume closes whatever was open. */
    @Test
    fun `a resume closes the span that was open`() {
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), resumed(CHAT, 4 * minute), paused(CHAT, 5 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(
            listOf(
                ForegroundSpan(GAME, 1 * minute, 4 * minute),
                ForegroundSpan(CHAT, 4 * minute, 5 * minute),
            ),
            spans,
        )
    }

    /**
     * Activities inside one app pause and resume around each other. Taking any pause as the end of
     * the session would cut a long session into a short one every time a second screen opened.
     */
    @Test
    fun `a pause for a package that is not the open one is ignored`() {
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), paused(CHAT, 3 * minute), paused(GAME, 8 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 8 * minute)), spans)
    }

    /** FR-3.3: the span ends when the screen went off, not when the poll happened to run. */
    @Test
    fun `screen off closes the open span at the moment it went off`() {
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), screenOff(3 * minute)),
            windowEndMillis = 8 * 60 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 3 * minute)), spans)
    }

    @Test
    fun `a screen off with nothing open produces nothing`() {
        assertTrue(SpanFolder.fold(listOf(screenOff(3 * minute)), windowEndMillis = 10 * minute).isEmpty())
    }

    /**
     * A session still running when the poll fires is closed at the window end. The next window opens
     * its own span from its own `RESUMED`, so nothing is double-counted and nothing carries.
     */
    @Test
    fun `a span still open at the end of the window is closed there`() {
        val spans = SpanFolder.fold(listOf(resumed(GAME, 1 * minute)), windowEndMillis = 10 * minute)

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 10 * minute)), spans)
    }

    @Test
    fun `an app resumed after the window end contributes nothing`() {
        val spans = SpanFolder.fold(listOf(resumed(GAME, 20 * minute)), windowEndMillis = 10 * minute)

        assertTrue(spans.isEmpty())
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
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 5 * minute), paused(GAME, 5 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertTrue(spans.isEmpty())
    }

    @Test
    fun `a resume with no package name is ignored and leaves the open span alone`() {
        val spans = SpanFolder.fold(
            listOf(resumed(GAME, 1 * minute), resumed("", 4 * minute), paused(GAME, 8 * minute)),
            windowEndMillis = 10 * minute,
        )

        assertEquals(listOf(ForegroundSpan(GAME, 1 * minute, 8 * minute)), spans)
    }

    @Test
    fun `no events at all is no usage, not an open span`() {
        assertTrue(SpanFolder.fold(emptyList(), windowEndMillis = 10 * minute).isEmpty())
    }

    private fun resumed(pkg: String, at: Long) = ForegroundEvent(ForegroundEventKind.RESUMED, pkg, at)
    private fun paused(pkg: String, at: Long) = ForegroundEvent(ForegroundEventKind.PAUSED, pkg, at)
    private fun screenOff(at: Long) = ForegroundEvent(ForegroundEventKind.SCREEN_OFF, "", at)

    private companion object {
        const val GAME = "com.example.game"
        const val CHAT = "com.example.chat"
    }
}
