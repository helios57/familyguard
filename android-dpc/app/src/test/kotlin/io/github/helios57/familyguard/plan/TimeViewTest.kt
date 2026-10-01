package io.github.helios57.familyguard.plan

import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.TodayReport
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** FR-28 on the phone: the three kinds of time, and "Mehr Zeit erbitten". */
class TimeViewTest {

    private val json = Json { ignoreUnknownKeys = true }

    private fun report(
        used: Int = 40,
        daily: Int = 90,
        extra: Int = 0,
        bonus: Int = 0,
        reason: String = "",
    ) = TodayReport(
        usedMinutes = used, limitMinutes = maxOf(0, daily + extra), bonusMinutes = extra,
        dailyLimitMinutes = daily, earnedMinutesLeft = bonus, suspendReason = reason, nextChangeAt = "",
        apps = emptyList(),
    )

    private fun plan(vararg requests: DayTimeRequest, left: Int? = 3 - requests.size) =
        DayPlan(day = "2026-10-01", timeRequests = requests.toList(), timeRequestsLeft = left)

    @Test
    fun `what is left is the day's time and today's extra time not yet used, plus the Bonuszeit`() {
        val card = TimeView.card(report(used = 40, daily = 90, extra = 15, bonus = 20), null)
        assertEquals(90 + 15 - 40 + 20, card.leftMinutes)
        assertEquals(90, card.dailyMinutes)
        assertEquals(15, card.extraMinutes)
        assertEquals(20, card.bonusMinutes)
    }

    @Test
    fun `past the limit only the Bonuszeit is left, and a debt is not time`() {
        assertEquals(20, TimeView.card(report(used = 120, daily = 90, bonus = 20), null).leftMinutes)
        assertEquals(0, TimeView.card(report(used = 120, daily = 90, bonus = -10), null).leftMinutes)
    }

    @Test
    fun `with no daily limit nothing is left or missing`() {
        assertNull(TimeView.card(report(daily = 0), null).leftMinutes)
    }

    @Test
    fun `the Bonuszeit expiry is the soonest credit, and only while there is Bonuszeit`() {
        val credits = DayPlan(earned = DayEarned(credits = listOf(
            DayCredit("2026-09-30", "2026-10-06", 10), DayCredit("2026-09-28", "2026-10-04", 5),
        )))
        assertEquals("2026-10-04", TimeView.card(report(bonus = 15), credits).bonusExpiry?.expiresOn)
        assertNull(TimeView.card(report(bonus = 0), credits).bonusExpiry)
    }

    @Test
    fun `the button is offered while there is a limit to add to`() {
        assertEquals(TimeView.Ask.OFFER, TimeView.ask(report(), plan()))
        assertEquals(TimeView.Ask.OFFER, TimeView.ask(report(reason = EnforcementEngine.REASON_QUOTA), plan()))
        // A server from before FR-28 sends no count: the button is still offered and the server decides.
        assertEquals(TimeView.Ask.OFFER, TimeView.ask(report(), DayPlan()))
    }

    @Test
    fun `no button where Extrazeit would not open anything`() {
        assertEquals(TimeView.Ask.HIDDEN, TimeView.ask(null, plan()))
        assertEquals(TimeView.Ask.HIDDEN, TimeView.ask(report(daily = 0), plan()))
        assertEquals(TimeView.Ask.HIDDEN, TimeView.ask(report(reason = EnforcementEngine.REASON_PAUSED), plan()))
        assertEquals(TimeView.Ask.HIDDEN, TimeView.ask(report(reason = EnforcementEngine.REASON_BEDTIME), plan()))
    }

    @Test
    fun `a waiting request is said rather than asked again, and three a day is the end`() {
        val waiting = DayTimeRequest(id = "r1", minutes = 30, state = DayTimeRequest.OPEN)
        assertEquals(TimeView.Ask.WAITING, TimeView.ask(report(), plan(waiting)))
        val answered = DayTimeRequest(id = "r", minutes = 15, state = DayTimeRequest.DECLINED)
        assertEquals(TimeView.Ask.NONE_LEFT, TimeView.ask(report(), plan(answered, answered, answered)))
    }

    @Test
    fun `an answer is news once, and only about a request the phone saw waiting`() {
        val waiting = plan(DayTimeRequest(id = "r1", minutes = 30, state = DayTimeRequest.OPEN))
        val granted = plan(DayTimeRequest(id = "r1", minutes = 30, state = DayTimeRequest.GRANTED, grantedMinutes = 15))
        assertEquals(15, TimeView.newlyAnswered(waiting, granted)?.grantedMinutes)
        assertNull(TimeView.newlyAnswered(granted, granted))
        assertNull(TimeView.newlyAnswered(null, granted))
        assertNull(TimeView.newlyAnswered(waiting, waiting))
    }

    @Test
    fun `the server's request block parses`() {
        val plan = json.decodeFromString(DayPlan.serializer(), """
            {"day":"2026-10-01","groups":[],"earned":{"available_minutes":0,"spent_minutes":0,"left_minutes":0,"credits":[]},
             "time_requests":[{"id":"r1","child_id":"c","day":"2026-10-01","minutes":30,"note":"Film",
               "state":"GRANTED","granted_minutes":15,"requested_at":"2026-10-01T10:00:00Z"}],
             "time_requests_left":2}
        """.trimIndent())
        assertEquals(DayTimeRequest("r1", 30, "Film", DayTimeRequest.GRANTED, 15), plan.timeRequests.single())
        assertEquals(2, plan.timeRequestsLeft)
    }
}
