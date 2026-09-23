package io.github.helios57.familyguard.enforce

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-3.10: the reasons a child reads for each app come out of the same engine output the phone
 * enforces, named exactly as the console names them.
 */
class TodayReportTest {

    private val launcher = "com.sec.android.app.launcher"

    private fun input(
        limit: Int = 60,
        allowed: List<String> = emptyList(),
        blocked: List<String> = emptyList(),
        own: List<AppLimit> = emptyList(),
        installs: Boolean = true,
        installed: List<App> = listOf(App("com.example.game"), App("com.example.video"), App("com.whatsapp")),
        used: Int,
        byPackage: Map<String, Int>,
        bonus: Int = 0,
    ) = Input(
        settings = Settings(
            dailyLimitMinutes = limit, timezone = "Europe/Zurich", allowChildInstalls = installs,
            allowedPackages = allowed, blockedPackages = blocked, limitedPackages = own,
            bonusMinutes = bonus, bonusDay = if (bonus > 0) "2026-09-23" else "",
        ),
        installed = installed,
        usedMinutesToday = used,
        usedMinutesByPackage = byPackage,
        now = "2026-09-23T15:00:00+02:00",
    )

    private fun report(input: Input) =
        TodayReport.of(input, EnforcementEngine.compute(input), input.usedMinutesByPackage, setOf(launcher))

    @Test
    fun `a spent daily limit pauses what counts and names the limit as the reason`() {
        val r = report(input(
            allowed = listOf("com.whatsapp"), used = 60,
            byPackage = mapOf("com.example.game" to 50, "com.whatsapp" to 10, launcher to 57),
        ))

        assertEquals(EnforcementEngine.REASON_QUOTA, r.suspendReason)
        val game = r.apps.single { it.packageName == "com.example.game" }
        assertEquals(TodayReport.Block.QUOTA, game.blocked)
        assertEquals(TodayReport.Rule.COUNTS, game.rule)
        val chat = r.apps.single { it.packageName == "com.whatsapp" }
        assertNull("an always-free app is not paused by the daily limit", chat.blocked)
        assertEquals(TodayReport.Rule.ALWAYS_FREE, chat.rule)
        assertFalse("the home screen is shown, and shown as not counted", r.apps.single { it.packageName == launcher }.counted)
    }

    @Test
    fun `a preinstalled camera stays free when the limit is spent, a preinstalled browser does not`() {
        val camera = "com.sec.android.app.camera"
        val chrome = "com.android.chrome"
        val base = input(
            used = 60,
            installed = listOf(App("com.example.game"), App(camera, system = true, launchable = true),
                App(chrome, system = true, launchable = true)),
            byPackage = mapOf("com.example.game" to 40, camera to 5, chrome to 15),
        )
        val r = report(base.copy(settings = base.settings.copy(countedSystemPackages = listOf(chrome))))

        val cam = r.apps.single { it.packageName == camera }
        assertEquals(TodayReport.Rule.FREE_PREINSTALLED, cam.rule)
        assertNull("the camera is part of the phone, not screen time (FR-5.10)", cam.blocked)
        val browser = r.apps.single { it.packageName == chrome }
        assertEquals(TodayReport.Rule.COUNTS, browser.rule)
        assertEquals(TodayReport.Block.QUOTA, browser.blocked)
    }

    @Test
    fun `an app whose own allowance is spent says so, on a phone with time left`() {
        val r = report(input(
            own = listOf(AppLimit("com.example.video", 30)), used = 35,
            byPackage = mapOf("com.example.video" to 30, "com.example.game" to 5),
        ))

        assertEquals("", r.suspendReason)
        val video = r.apps.single { it.packageName == "com.example.video" }
        assertEquals(TodayReport.Block.APP_LIMIT, video.blocked)
        assertEquals(30, video.ownLimitMinutes)
        assertNull(r.apps.single { it.packageName == "com.example.game" }.blocked)
    }

    @Test
    fun `a parent's block and a pending app are told apart`() {
        val r = report(input(
            blocked = listOf("com.example.game"), installs = false,
            installed = listOf(App("com.example.game"), App("com.example.new", newSinceBaseline = true)),
            used = 2, byPackage = mapOf("com.example.game" to 2),
        ))

        assertEquals(TodayReport.Block.BLOCKED, r.apps.single { it.packageName == "com.example.game" }.blocked)
        val fresh = r.apps.single { it.packageName == "com.example.new" }
        assertEquals("a pending app is listed even with no use yet", TodayReport.Block.PENDING, fresh.blocked)
    }

    @Test
    fun `extra time today raises the limit the phone shows`() {
        val r = report(input(used = 60, byPackage = mapOf("com.example.game" to 60), bonus = 30))

        assertEquals(90, r.limitMinutes)
        assertEquals(30, r.bonusMinutes)
        assertEquals("", r.suspendReason)
        assertNull(r.apps.single().blocked)
    }

    @Test
    fun `most used first`() {
        val r = report(input(used = 40, byPackage = mapOf("com.example.game" to 5, "com.example.video" to 30)))
        assertTrue(r.apps.first().packageName == "com.example.video")
    }
}
