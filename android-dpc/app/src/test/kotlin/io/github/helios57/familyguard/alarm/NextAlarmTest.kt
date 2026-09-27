package io.github.helios57.familyguard.alarm

import java.time.Instant
import java.time.ZoneId
import java.time.ZonedDateTime
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** FR-23 on the phone: when the alarm rings next, computed from the rule the server sends. */
class NextAlarmTest {

    private val json = Json { ignoreUnknownKeys = true }
    private val zurich = ZoneId.of("Europe/Zurich")
    private val weekdays = listOf("06:30", "06:30", "06:30", "06:30", "07:00", "", "")

    private fun at(local: String): Instant = ZonedDateTime.of(java.time.LocalDateTime.parse(local), zurich).toInstant()
    private fun schedule(week: List<String> = weekdays, vararg changes: AlarmDayChange) =
        AlarmSchedule(timezone = "Europe/Zurich", weekdays = week, overrides = changes.toList())

    @Test
    fun `the server's alarm block parses, with no time meaning no alarm that day`() {
        val parsed = json.decodeFromString(AlarmSchedule.serializer(), """
            {"timezone":"Europe/Zurich","weekdays":["06:30","","","","","",""],
             "overrides":[{"day":"2026-09-28","time":null},{"day":"2026-09-29","time":"05:45"}]}
        """.trimIndent())
        assertEquals("06:30", parsed.weekdays[0])
        assertNull(parsed.overrides[0].time)
        assertEquals("05:45", parsed.overrides[1].time)
    }

    @Test
    fun `a weekend evening rings on Monday morning`() {
        // 2026-09-27 is a Sunday.
        assertEquals(at("2026-09-28T06:30"), NextAlarm.next(schedule(), at("2026-09-27T20:00")))
    }

    @Test
    fun `once today's time has passed it is the next day that rings`() {
        assertEquals(at("2026-09-29T06:30"), NextAlarm.next(schedule(), at("2026-09-28T06:30")))
        assertEquals(at("2026-09-28T06:30"), NextAlarm.next(schedule(), at("2026-09-28T06:29:59")))
    }

    @Test
    fun `Friday has its own time and the weekend is skipped`() {
        assertEquals(at("2026-10-02T07:00"), NextAlarm.next(schedule(), at("2026-10-01T08:00")))
        assertEquals(at("2026-10-05T06:30"), NextAlarm.next(schedule(), at("2026-10-02T07:01")))
    }

    @Test
    fun `a date change replaces the weekday's time`() {
        val s = schedule(weekdays, AlarmDayChange("2026-09-28", "05:45"))
        assertEquals(at("2026-09-28T05:45"), NextAlarm.next(s, at("2026-09-27T20:00")))
    }

    @Test
    fun `a date change to no alarm skips that day`() {
        val s = schedule(weekdays, AlarmDayChange("2026-09-28", null))
        assertEquals(at("2026-09-29T06:30"), NextAlarm.next(s, at("2026-09-27T20:00")))
    }

    @Test
    fun `a date change can ring on a day the week leaves silent`() {
        val s = schedule(weekdays, AlarmDayChange("2026-10-03", "09:00"))
        assertEquals(at("2026-10-03T09:00"), NextAlarm.next(s, at("2026-10-02T08:00")))
    }

    @Test
    fun `no alarm at all is null, not a guess`() {
        assertNull(NextAlarm.next(schedule(List(7) { "" }), at("2026-09-27T20:00")))
    }

    @Test
    fun `an unreadable time or zone rings nothing rather than something wrong`() {
        assertNull(NextAlarm.next(schedule(List(7) { "6:3x" }), at("2026-09-27T20:00")))
        assertNull(NextAlarm.next(AlarmSchedule(timezone = "Mars/Olympus", weekdays = weekdays), at("2026-09-27T20:00")))
    }

    @Test
    fun `a time that does not exist on the spring-forward day rings when the clock resumes`() {
        // Europe/Zurich skips 02:00–03:00 on 2027-03-28.
        val s = schedule(List(7) { "02:30" })
        assertEquals(at("2027-03-28T03:30"), NextAlarm.next(s, at("2027-03-28T00:00")))
    }

    @Test
    fun `a time that happens twice on the fall-back day rings the first time only`() {
        // Europe/Zurich repeats 02:00–03:00 on 2026-10-25; the first 02:30 is +02:00.
        val s = schedule(List(7) { "02:30" })
        val first = Instant.parse("2026-10-25T00:30:00Z")
        assertEquals(first, NextAlarm.next(s, Instant.parse("2026-10-24T22:00:00Z")))
        // Once it has rung, the repeated hour does not ring it again: the next is the following day.
        assertEquals(at("2026-10-26T02:30"), NextAlarm.next(s, first))
    }

    @Test
    fun `the Heute line says today, tomorrow or the weekday, in the profile's zone`() {
        val s = schedule()
        assertEquals(AlarmLine(AlarmLine.Day.TODAY, "06:30", java.time.DayOfWeek.MONDAY),
            AlarmLine.of(s, at("2026-09-28T06:30"), at("2026-09-28T05:00")))
        assertEquals(AlarmLine(AlarmLine.Day.TOMORROW, "06:30", java.time.DayOfWeek.MONDAY),
            AlarmLine.of(s, at("2026-09-28T06:30"), at("2026-09-27T23:59")))
        assertEquals(AlarmLine(AlarmLine.Day.LATER, "07:00", java.time.DayOfWeek.FRIDAY),
            AlarmLine.of(s, at("2026-10-02T07:00"), at("2026-09-28T07:00")))
    }

    @Test
    fun `not during holidays skips a holiday's dates, but a date changed on its own still rings`() {
        val holiday = AlarmHoliday(startsOn = "2026-10-05", endsOn = "2026-10-09")
        val skip = schedule().copy(skipHolidays = true, holidays = listOf(holiday))
        // Friday 2026-10-02 evening: the whole next week is a holiday, so the next ring is 12 Oct.
        assertEquals(at("2026-10-12T06:30"), NextAlarm.next(skip, at("2026-10-02T20:00")))
        // Without the flag the holiday changes nothing.
        assertEquals(at("2026-10-05T06:30"), NextAlarm.next(schedule().copy(holidays = listOf(holiday)), at("2026-10-02T20:00")))
        // A change set for a date inside the holiday is explicit, and rings.
        val changed = skip.copy(overrides = listOf(AlarmDayChange("2026-10-07", "09:00")))
        assertEquals(at("2026-10-07T09:00"), NextAlarm.next(changed, at("2026-10-02T20:00")))
    }

    @Test
    fun `the holidays and the flag parse from the server's alarm block`() {
        val parsed = json.decodeFromString(AlarmSchedule.serializer(), """
            {"timezone":"Europe/Zurich","weekdays":["06:30","","","","","",""],"overrides":[],"skip_holidays":true,
             "holidays":[{"id":"h","title":"Herbstferien","starts_on":"2026-10-05","ends_on":"2026-10-09"}]}
        """.trimIndent())
        assertEquals(true, parsed.skipHolidays)
        assertEquals("2026-10-09", parsed.holidays.single().endsOn)
    }
}
