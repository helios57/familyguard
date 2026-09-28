package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * FR-26.4: after 5 minutes of screen off, with nothing playing, the filter carries only DNS; the screen
 * coming on, or sound starting, brings the full route back. A parent's own DNS-only choice is never
 * widened.
 */
class ScreenRouteTest {

    private val now = 10_000_000L

    private fun route(
        chosen: RouteMode = RouteMode.FULL,
        screenOn: Boolean = false,
        offFor: Long? = null,
        audio: Boolean = false,
        running: RouteMode? = RouteMode.FULL,
    ) = ScreenRoute.effective(chosen, screenOn, offFor?.let { now - it }, now, audio, running)

    @Test
    fun `the screen on is the full route`() = assertEquals(RouteMode.FULL, route(screenOn = true))

    @Test
    fun `just under five minutes off is still the full route`() =
        assertEquals(RouteMode.FULL, route(offFor = 5 * 60_000L - 1))

    @Test
    fun `five minutes off is DNS only`() = assertEquals(RouteMode.DNS_ONLY, route(offFor = 5 * 60_000L))

    @Test
    fun `a screen off since before the service knew is DNS only`() =
        assertEquals(RouteMode.DNS_ONLY, route(offFor = null))

    @Test
    fun `sound playing holds the full route with the screen off`() =
        assertEquals(RouteMode.FULL, route(offFor = 60 * 60_000L, audio = true))

    @Test
    fun `sound starting after the narrowing does not widen it, which would cut the stream`() =
        assertEquals(RouteMode.DNS_ONLY, route(offFor = 60 * 60_000L, audio = true, running = RouteMode.DNS_ONLY))

    @Test
    fun `the screen on widens a narrowed route`() =
        assertEquals(RouteMode.FULL, route(screenOn = true, running = RouteMode.DNS_ONLY))

    @Test
    fun `a tunnel coming up with the screen long off and silence starts narrow`() =
        assertEquals(RouteMode.DNS_ONLY, route(offFor = 60 * 60_000L, running = null))

    @Test
    fun `a parent's DNS-only choice stays DNS only with the screen on`() =
        assertEquals(RouteMode.DNS_ONLY, route(chosen = RouteMode.DNS_ONLY, screenOn = true))

    @Test
    fun `the next change is the end of the five minutes, and only while it is ahead`() {
        assertEquals(now - 60_000L + 5 * 60_000L,
            ScreenRoute.nextChangeAtElapsed(RouteMode.FULL, false, now - 60_000L, now, false))
        assertNull(ScreenRoute.nextChangeAtElapsed(RouteMode.FULL, true, null, now, false))
        assertNull(ScreenRoute.nextChangeAtElapsed(RouteMode.FULL, false, now - 6 * 60_000L, now, false))
        assertNull(ScreenRoute.nextChangeAtElapsed(RouteMode.DNS_ONLY, false, now - 60_000L, now, false))
        assertNull("sound playing waits for the sound to stop, not for a clock",
            ScreenRoute.nextChangeAtElapsed(RouteMode.FULL, false, now - 60_000L, now, true))
    }
}
