package io.github.helios57.familyguard.plan

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** FR-22 on the phone: the day's plan as the server sends it, and what the Heute screen offers. */
class DayPlanTest {

    private val json = Json { ignoreUnknownKeys = true }

    @Test
    fun `the server's today block parses, including the fields the phone does not use`() {
        val plan = json.decodeFromString(DayPlan.serializer(), """
            {"day":"2026-09-28","groups":[{"id":"g1","title":"Tag","starts_at":"07:00","ends_at":"20:00",
             "earned_minutes":30,"open":true,"credited_minutes":0,
             "tasks":[{"id":"t1","title":"Katze füttern","note":"","state":"OPEN","reported_at":null}]}],
             "earned":{"available_minutes":30,"spent_minutes":5,"left_minutes":25,
             "credits":[{"earned_on":"2026-09-28","expires_on":"2026-10-04","minutes":30}]}}
        """.trimIndent())
        assertEquals("Katze füttern", plan.groups.single().tasks.single().title)
        assertEquals(25, plan.earned.leftMinutes)
    }

    @Test
    fun `a task can be reported inside its window while it is open or was not accepted`() {
        val open = DayGroup(id = "g", title = "Tag", open = true)
        val closed = open.copy(open = false)
        assertTrue(DayPlanView.canReport(open, DayTask(id = "t", state = DayTask.OPEN)))
        assertTrue("a rejected task can be reported again", DayPlanView.canReport(open, DayTask(id = "t", state = DayTask.REJECTED)))
        assertFalse(DayPlanView.canReport(open, DayTask(id = "t", state = DayTask.REPORTED)))
        assertFalse(DayPlanView.canReport(open, DayTask(id = "t", state = DayTask.CONFIRMED)))
        assertFalse("outside its window nothing can be reported", DayPlanView.canReport(closed, DayTask(id = "t", state = DayTask.OPEN)))
    }

    @Test
    fun `the credit that runs out first is the one named`() {
        val credits = listOf(
            DayCredit(earnedOn = "2026-09-24", expiresOn = "2026-09-30", minutes = 10),
            DayCredit(earnedOn = "2026-09-22", expiresOn = "2026-09-28", minutes = 0),
            DayCredit(earnedOn = "2026-09-23", expiresOn = "2026-09-29", minutes = 20),
        )
        assertEquals("2026-09-29", DayPlanView.soonestExpiry(credits)?.expiresOn)
        assertNull(DayPlanView.soonestExpiry(emptyList()))
    }
}
