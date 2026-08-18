package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

/**
 * Which day a minute of screen time counts against.
 *
 * The whole subject is a boundary, and getting it wrong is not visible from either side: minutes
 * moved off yesterday give a child a second allowance, and minutes moved onto today spend one they
 * never had. Neither shows up as an error anywhere — the console just shows a number.
 */
class DayAttributionTest {

    private val zurich = ZoneId.of("Europe/Zurich")

    private fun at(text: String): Long = ZonedDateTime.parse(text).toInstant().toEpochMilli()

    @Test
    fun `a span inside one day is credited to that day`() {
        val spans = listOf(span("com.example.game", "2026-08-17T20:00+02:00", "2026-08-17T20:30+02:00"))

        val byDay = DayAttribution.byDay(spans, zurich)

        assertEquals(setOf("2026-08-17"), byDay.keys)
        assertEquals(30 * 60_000L, byDay.getValue("2026-08-17").getValue("com.example.game"))
    }

    /**
     * The case the 00:02 poll produces. A window that began at 23:50 belongs mostly to yesterday, and
     * crediting all of it to the day the poll landed in hands back quota that was already spent.
     */
    @Test
    fun `a span crossing midnight is split at local midnight`() {
        val spans = listOf(span("com.example.game", "2026-08-17T23:50+02:00", "2026-08-18T00:20+02:00"))

        val byDay = DayAttribution.byDay(spans, zurich)

        assertEquals(listOf("2026-08-17", "2026-08-18"), byDay.keys.toList())
        assertEquals(10 * 60_000L, byDay.getValue("2026-08-17").getValue("com.example.game"))
        assertEquals(20 * 60_000L, byDay.getValue("2026-08-18").getValue("com.example.game"))
    }

    /** Nothing is lost or invented by the split: the pieces still sum to the span. */
    @Test
    fun `a span spanning three days keeps its total`() {
        val spans = listOf(span("com.example.game", "2026-08-16T22:00+02:00", "2026-08-18T02:00+02:00"))

        val byDay = DayAttribution.byDay(spans, zurich)

        assertEquals(listOf("2026-08-16", "2026-08-17", "2026-08-18"), byDay.keys.toList())
        val total = byDay.values.sumOf { it.values.sum() }
        assertEquals(28 * 60 * 60_000L, total)
        assertEquals(24 * 60 * 60_000L, byDay.getValue("2026-08-17").getValue("com.example.game"))
    }

    /**
     * The zone is the *policy's*, and a device sitting in another one must still post the family's
     * day keys. 23:50 in Zurich is 21:50 UTC, so the same instants land on one day or two depending
     * on which zone is asked — which is exactly the mistake that files a child's evening under a key
     * the server's quota never reads.
     */
    @Test
    fun `the day boundary follows the policy zone, not the device`() {
        val spans = listOf(span("com.example.game", "2026-08-17T23:50+02:00", "2026-08-18T00:20+02:00"))

        val utc = DayAttribution.byDay(spans, ZoneId.of("UTC"))

        assertEquals("in UTC these instants are one day, not two", setOf("2026-08-17"), utc.keys)
    }

    /**
     * Switzerland's spring transition: 2026-03-29 has no 02:00–03:00 at all, so "the start of the
     * next day" is the only correct way to find midnight. Adding 24 hours would put the cut an hour
     * out and quietly move sixty minutes across the boundary twice a year.
     */
    @Test
    fun `a DST day is cut where the day actually starts`() {
        val spans = listOf(span("com.example.game", "2026-03-28T23:30+01:00", "2026-03-29T00:30+01:00"))

        val byDay = DayAttribution.byDay(spans, zurich)

        assertEquals(30 * 60_000L, byDay.getValue("2026-03-28").getValue("com.example.game"))
        assertEquals(30 * 60_000L, byDay.getValue("2026-03-29").getValue("com.example.game"))
    }

    @Test
    fun `two spans of the same package on one day are summed`() {
        val spans = listOf(
            span("com.example.game", "2026-08-17T10:00+02:00", "2026-08-17T10:10+02:00"),
            span("com.example.game", "2026-08-17T14:00+02:00", "2026-08-17T14:05+02:00"),
        )

        val byDay = DayAttribution.byDay(spans, zurich)

        assertEquals(15 * 60_000L, byDay.getValue("2026-08-17").getValue("com.example.game"))
    }

    /** A zero-length span and a blank package are dropped rather than stored as an empty entry. */
    @Test
    fun `spans with no duration and no package are dropped`() {
        val spans = listOf(
            span("com.example.game", "2026-08-17T10:00+02:00", "2026-08-17T10:00+02:00"),
            span("", "2026-08-17T11:00+02:00", "2026-08-17T11:10+02:00"),
        )

        assertTrue(DayAttribution.byDay(spans, zurich).isEmpty())
    }

    // ---- the key and the zone ------------------------------------------------------------------

    /**
     * The key the device posts under and the key [byDay] produces have to be the same string. They
     * are produced by one function for that reason: a mismatch reads as a day with no usage, not as
     * an error, so the quota would simply never be reached.
     */
    @Test
    fun `the day key matches the key the attribution produces`() {
        val instant = at("2026-08-17T23:50+02:00")
        val spans = listOf(span("com.example.game", "2026-08-17T23:50+02:00", "2026-08-17T23:55+02:00"))

        assertEquals(
            DayAttribution.byDay(spans, zurich).keys.single(),
            DayAttribution.key(instant, zurich),
        )
    }

    @Test
    fun `the day key follows the zone it is given`() {
        val instant = at("2026-08-18T00:20+02:00")

        assertEquals("2026-08-18", DayAttribution.key(instant, zurich))
        assertEquals("2026-08-17", DayAttribution.key(instant, ZoneId.of("UTC")))
    }

    /**
     * An unreadable zone is null, never the device's own. Falling back would attribute usage to a day
     * the server's quota does not read — and the fallback would be invisible, because a device in the
     * family's own timezone would behave identically.
     */
    @Test
    fun `an unknown or empty timezone has no zone at all`() {
        assertEquals(zurich, DayAttribution.zoneOf("Europe/Zurich"))
        assertEquals(zurich, DayAttribution.zoneOf("  Europe/Zurich  "))
        assertNull(DayAttribution.zoneOf("Middle/Earth"))
        assertNull(DayAttribution.zoneOf(""))
    }

    private fun span(pkg: String, from: String, to: String) = ForegroundSpan(pkg, at(from), at(to))
}
