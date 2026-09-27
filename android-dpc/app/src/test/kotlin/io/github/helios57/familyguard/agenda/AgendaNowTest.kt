package io.github.helios57.familyguard.agenda

import java.time.LocalDateTime
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** FR-24.5 on the phone: what is on now and next, from the two days the server sends. */
class AgendaNowTest {

    private val json = Json { ignoreUnknownKeys = true }
    private val school = AgendaItem(entryId = "s", title = "Schule", place = "Schulhaus", startsAt = "08:00", endsAt = "12:00")
    private val dentist = AgendaItem(entryId = "z", title = "Zahnarzt", startsAt = "14:00", endsAt = "14:30")
    private val training = AgendaItem(entryId = "t", title = "Training", startsAt = "17:00", endsAt = "18:30", optional = true)
    private val block = AgendaBlock(listOf(
        AgendaDay(day = "2026-10-07", items = listOf(school, dentist, training)),
        AgendaDay(day = "2026-10-08", holiday = "Herbstferien", items = emptyList()),
    ))

    private fun at(s: String) = LocalDateTime.parse(s)

    @Test
    fun `the server's agenda block parses`() {
        val parsed = json.decodeFromString(AgendaBlock.serializer(), """
            {"days":[{"day":"2026-10-07","holiday":"","items":[{"entry_id":"s","title":"Schule","place":"Schulhaus",
             "starts_at":"08:00","ends_at":"12:00","optional":false}]},{"day":"2026-10-08","holiday":"Herbstferien","items":[]}]}
        """.trimIndent())
        assertEquals("Schulhaus", parsed.days[0].items[0].place)
        assertEquals("Herbstferien", parsed.days[1].holiday)
    }

    @Test
    fun `before the first entry nothing is on and the first is next`() {
        val v = AgendaNow.of(block, at("2026-10-07T07:15"))
        assertNull(v.current)
        assertEquals(school, v.next)
    }

    @Test
    fun `inside an entry it is on now, and the following one is next`() {
        val v = AgendaNow.of(block, at("2026-10-07T09:00"))
        assertEquals(school, v.current)
        assertEquals(dentist, v.next)
    }

    @Test
    fun `an entry is over at its end minute`() {
        val v = AgendaNow.of(block, at("2026-10-07T12:00"))
        assertNull(v.current)
        assertEquals(dentist, v.next)
    }

    @Test
    fun `after the last entry there is nothing next today`() {
        val v = AgendaNow.of(block, at("2026-10-07T19:00"))
        assertNull(v.current)
        assertNull(v.next)
        assertEquals("Herbstferien", v.tomorrow?.holiday)
    }

    @Test
    fun `a phone offline past midnight shows the day that is today, and no tomorrow it was not sent`() {
        val v = AgendaNow.of(block, at("2026-10-08T09:00"))
        assertEquals("2026-10-08", v.today?.day)
        assertNull(v.tomorrow)
        assertNull(v.next)
    }

    @Test
    fun `a block for other days shows nothing rather than the wrong day`() {
        val v = AgendaNow.of(block, at("2026-10-12T09:00"))
        assertNull(v.today)
        assertNull(v.tomorrow)
    }

    /** FR-25: an all-day calendar event is not "now" or "next" — it is the day, said on its own line. */
    @Test
    fun `an all-day event is listed for the day, and is neither now nor next`() {
        val trip = AgendaItem(entryId = "", title = "Schulreise", startsAt = "00:00", endsAt = "23:59", allDay = true, source = "calendar")
        val day = AgendaBlock(listOf(AgendaDay(day = "2026-10-08", items = listOf(trip, school.copy(startsAt = "13:00", endsAt = "15:00")))))
        val v = AgendaNow.of(day, at("2026-10-08T09:00"))
        assertNull(v.current)
        assertEquals("Schule", v.next?.title)
        assertEquals(listOf("Schulreise"), v.allDay.map { it.title })
    }

    @Test
    fun `the calendar's fields parse`() {
        val parsed = json.decodeFromString(AgendaBlock.serializer(), """
            {"days":[{"day":"2026-10-08","holiday":"","items":[{"entry_id":"","title":"Schulreise","place":"",
             "starts_at":"00:00","ends_at":"23:59","optional":false,"all_day":true,"source":"calendar"}]}]}
        """.trimIndent())
        assertEquals(true, parsed.days[0].items[0].allDay)
        assertEquals("calendar", parsed.days[0].items[0].source)
    }
}
