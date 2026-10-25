package io.github.helios57.familyguard.usage

import io.github.helios57.familyguard.enforce.App
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.enforce.Settings
import io.github.helios57.familyguard.enforce.TodayReport
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/** FR-3.10 and FR-30.7: the Heute screen combines the phone's own measurement exactly as the service does. */
class TodayReportReaderTest {

    private val film = "com.example.video"
    private val letters = "org.example.learn.letters"

    /** Offline, nothing from the server: on at 0, and 10 minutes learned on this phone alone. */
    private val cached = Input(
        settings = Settings(
            dailyLimitMinutes = 0, dailyLimitEnabled = true, timezone = "Europe/Zurich",
            learningPrefixes = listOf("org.example.learn."),
        ),
        installed = listOf(App(film), App(letters)),
        now = "2026-10-25T15:00:00+01:00",
    )

    @Test
    fun `what the phone learned offline is on the Heute screen, and opens what it opens for the service`() {
        val effective = TodayReportReader.effective(
            cached, now = cached.now, byPackage = mapOf(letters to 10),
            localCounted = 0, localEarned = 0, localLearning = 10,
        )
        val state = EnforcementEngine.compute(effective)
        val report = TodayReport.of(effective, state, mapOf(letters to 10), emptySet())

        assertEquals(10, report.learnedMinutes)
        assertFalse("10 learned minutes open the film app", film in state.suspendedPackages)
        assertEquals("", report.suspendReason)
    }

    @Test
    fun `the server's figure wins when it is the larger, never the sum`() {
        val effective = TodayReportReader.effective(
            cached.copy(learningMinutesToday = 12), now = cached.now, byPackage = emptyMap(),
            localCounted = 0, localEarned = 0, localLearning = 10,
        )
        assertEquals(12, effective.learningMinutesToday)
    }
}
