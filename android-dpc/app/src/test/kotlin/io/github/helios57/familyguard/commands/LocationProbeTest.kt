package io.github.helios57.familyguard.commands

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

private class FakeLocationSource(
    private val permitted: Boolean = true,
    private val fresh: Fix? = null,
    private val cached: Fix? = null,
    private val throwOn: Set<String> = emptySet(),
) : LocationSource {
    var freshRequests = 0
        private set
    var lastTimeout: Long? = null
        private set

    private fun maybeThrow(call: String) {
        if (call in throwOn) throw SecurityException("$call refused")
    }

    override fun permitted(): Boolean {
        maybeThrow("permitted")
        return permitted
    }

    override fun freshFix(timeoutMillis: Long): Fix? {
        freshRequests++
        lastTimeout = timeoutMillis
        maybeThrow("freshFix")
        return fresh
    }

    override fun lastKnownFix(): Fix? {
        maybeThrow("lastKnownFix")
        return cached
    }
}

private const val NOW = 1_700_000_000_000L

private fun fix(atMillis: Long, lat: Double = 47.37, lon: Double = 8.54) =
    Fix(latitude = lat, longitude = lon, accuracyM = 12.0, capturedAtEpochMillis = atMillis)

class LocationProbeTest {

    @Test
    fun `a fresh fix is reported as fresh, with no age`() {
        val source = FakeLocationSource(fresh = fix(NOW))

        val result = LocationProbe(source, { NOW }).probe()

        val located = result as ProbeResult.Located
        assertTrue(located.fresh)
        assertEquals(0L, located.ageMillis)
        assertEquals(47.37, located.fix.latitude, 0.0)
        assertEquals(LocationProbe.DEFAULT_TIMEOUT_MILLIS, source.lastTimeout)
    }

    @Test
    fun `no fresh fix falls back to the cached one, with its true age`() {
        val twentyMinutesAgo = NOW - 20 * 60 * 1000
        val source = FakeLocationSource(fresh = null, cached = fix(twentyMinutesAgo))

        val result = LocationProbe(source, { NOW }).probe()

        val located = result as ProbeResult.Located
        // Answering "no position available" while the platform holds one from twenty minutes ago is
        // withholding the only thing the parent asked for.
        assertFalse(located.fresh)
        assertEquals(20 * 60 * 1000L, located.ageMillis)
        // Never re-dated to now: the console must show "here, twenty minutes ago", not a false
        // present tense a parent would walk somewhere on.
        assertEquals(twentyMinutesAgo, located.fix.capturedAtEpochMillis)
    }

    @Test
    fun `no permission is its own answer, and not the same as no position`() {
        val source = FakeLocationSource(permitted = false, cached = fix(NOW))

        val result = LocationProbe(source, { NOW }).probe()

        val unavailable = result as ProbeResult.Unavailable
        // "The permission was revoked" is something a parent can fix; "the phone is in a basement"
        // is not. Both would otherwise arrive as the same empty answer.
        assertTrue(unavailable.reason, unavailable.reason.contains("permission"))
        // And the hardware is never touched — asking without a permission is what throws.
        assertEquals(0, source.freshRequests)
    }

    @Test
    fun `no fix and no cached position says so rather than inventing one`() {
        val source = FakeLocationSource(fresh = null, cached = null)

        val result = LocationProbe(source, { NOW }, timeoutMillis = 30_000).probe()

        val unavailable = result as ProbeResult.Unavailable
        assertTrue(unavailable.reason, unavailable.reason.contains("30s"))
        assertTrue(unavailable.reason, unavailable.reason.contains("no last known position"))
    }

    @Test
    fun `a fresh fix that throws still falls through to the cached one`() {
        val source = FakeLocationSource(throwOn = setOf("freshFix"), cached = fix(NOW - 60_000))

        val result = LocationProbe(source, { NOW }).probe()

        // A permission revoked between the check and the request is exactly this path, and losing
        // the cached answer to it would cost the parent the fix the platform was already holding.
        val located = result as ProbeResult.Located
        assertFalse(located.fresh)
        assertEquals(60_000L, located.ageMillis)
    }

    @Test
    fun `a permission check that throws is reported as unreadable, not as denied`() {
        val source = FakeLocationSource(throwOn = setOf("permitted"))

        val result = LocationProbe(source, { NOW }).probe()

        val unavailable = result as ProbeResult.Unavailable
        assertTrue(unavailable.reason, unavailable.reason.contains("could not be read"))
    }

    @Test
    fun `a fix dated in the future reports an age of zero, never a negative one`() {
        val source = FakeLocationSource(fresh = null, cached = fix(NOW + 3 * 60 * 1000))

        val result = LocationProbe(source, { NOW }).probe()

        // A clock that moved — the phone's or the GNSS receiver's. "-3 minutes ago" in front of a
        // parent reads as a broken console rather than as what it is.
        assertEquals(0L, (result as ProbeResult.Located).ageMillis)
    }
}
