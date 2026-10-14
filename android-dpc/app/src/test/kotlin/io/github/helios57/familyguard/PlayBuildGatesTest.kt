package io.github.helios57.familyguard

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import java.io.File
import org.junit.Test

/**
 * FR-15.8: the two builds differ in exactly the places the switch says, and the manifest agrees with
 * the code about which build this is.
 *
 * The suite is meant to be run twice — `testDebugUnitTest` and `testDebugUnitTest -PplayBuild=true`
 * — and every assertion here is written for whichever build it finds itself in. The merged manifest
 * on the classpath is the one this variant ships (see `stage*MergedManifest` in build.gradle.kts), so
 * a constant that says "Play" over a manifest that still carries what Play refuses is caught here,
 * and so is the reverse: an overlay that leaked into the self-hosted build would take its self-update
 * permission with it, which is a phone that never updates and says nothing.
 *
 * The handlers' answers when a gate is closed are in CommandHandlersTest, in both directions,
 * whichever build runs it.
 */
class PlayBuildGatesTest {

    private val merged: String =
        (
            javaClass.getResourceAsStream("/MergedAndroidManifest.xml")?.bufferedReader()?.readText()
                ?: throw AssertionError("the merged manifest is not on the test classpath")
            ).replace(Regex("<!--.*?-->", RegexOption.DOT_MATCHES_ALL), "")

    private fun declaresPermission(name: String): Boolean =
        Regex("""<uses-permission[^>]*android:name="${Regex.escape(name)}"""").containsMatchIn(merged)

    private val vpnService: Boolean
        get() = merged.contains("""android:name="io.github.helios57.familyguard.filter.AdFilterVpnService"""")

    @Test
    fun `every feature gate follows the one switch`() {
        // All of them, in one place: a gate that does not move with PLAY_BUILD is a Play binary
        // with a self-hosted feature in it, or a self-hosted one with a feature missing.
        val available = !BuildConfig.PLAY_BUILD
        assertEquals("AD_FILTER_AVAILABLE", available, BuildConfig.AD_FILTER_AVAILABLE)
        assertEquals("SELF_UPDATE_AVAILABLE", available, BuildConfig.SELF_UPDATE_AVAILABLE)
        assertEquals("REMOTE_DEBUG_AVAILABLE", available, BuildConfig.REMOTE_DEBUG_AVAILABLE)
        assertEquals("QUIET_RUNNING_NOTICE", available, BuildConfig.QUIET_RUNNING_NOTICE)
    }

    @Test
    fun `the merged manifest carries what this build uses and nothing Play refuses`() {
        // The positive control first: a manifest that was not read declares nothing, which is the
        // same shape as a Play manifest with everything stripped.
        assertTrue(
            "the merged manifest does not declare INTERNET; this test is not reading a manifest",
            declaresPermission("android.permission.INTERNET"),
        )
        val selfHosted = !BuildConfig.PLAY_BUILD
        assertEquals(
            "UPDATE_PACKAGES_WITHOUT_USER_ACTION in a ${build()} build's manifest",
            selfHosted,
            declaresPermission("android.permission.UPDATE_PACKAGES_WITHOUT_USER_ACTION"),
        )
        assertEquals(
            "USE_EXACT_ALARM in a ${build()} build's manifest",
            selfHosted,
            declaresPermission("android.permission.USE_EXACT_ALARM"),
        )
        assertEquals("AdFilterVpnService in a ${build()} build's manifest", selfHosted, vpnService)
        assertEquals(
            "BIND_VPN_SERVICE in a ${build()} build's manifest; only the ad filter needs it",
            selfHosted,
            merged.contains("android.permission.BIND_VPN_SERVICE"),
        )
        // What the Play build keeps, and needs: the exact-alarm switch it falls back to.
        assertTrue(
            "SCHEDULE_EXACT_ALARM is gone; without USE_EXACT_ALARM that is the only exact alarm left",
            declaresPermission("android.permission.SCHEDULE_EXACT_ALARM"),
        )
    }

    @Test
    fun `both builds declare themselves a child-monitoring tool`() {
        // Play's Stalkerware and monitoring policy, on every version code. True of both builds, so
        // asserted in both.
        val meta = Regex("""<meta-data\s+android:name="isMonitoringTool"\s+android:value="([^"]*)"""")
            .find(merged)
        assertTrue("isMonitoringTool is missing from the ${build()} build's manifest", meta != null)
        assertEquals("child_monitoring", meta!!.groupValues[1])
        assertFalse(
            "isMonitoringTool is declared more than once",
            Regex("""android:name="isMonitoringTool"""").findAll(merged).count() > 1,
        )
    }

    /**
     * The two gates that live in ConnectionService, where no JVM test can start the service: read
     * from the source, in the manner of ManifestAndPlatformCallsTest.
     */
    @Test
    fun `the service arms the update timer and picks the notice only through the gates`() {
        val service = sequenceOf("src/main", "app/src/main", "android-dpc/app/src/main")
            .map { File(it, "kotlin/io/github/helios57/familyguard/sync/ConnectionService.kt") }
            .firstOrNull { it.isFile }
            ?: throw AssertionError("ConnectionService.kt was not found from ${File(".").absolutePath}")
        val code = service.readText().replace("\r\n", "\n")

        // The timer: `checkUpdate` is the only route from the alarm or a sync to the updater, so it
        // is assigned once, and that once is inside the gate.
        assertEquals(
            "checkUpdate is assigned an updater in more than one place",
            1,
            Regex("""checkUpdate = \{""").findAll(code).count(),
        )
        assertTrue(
            "the automatic update check is armed outside `if (BuildConfig.SELF_UPDATE_AVAILABLE)`",
            Regex("""if \(BuildConfig\.SELF_UPDATE_AVAILABLE\) \{\s*checkUpdate = \{""").containsMatchIn(code),
        )

        // The notice: the Play build's is on its own channel, at LOW, shown on the lock screen.
        assertTrue(
            "notification() no longer chooses the notice by QUIET_RUNNING_NOTICE",
            code.contains("if (BuildConfig.QUIET_RUNNING_NOTICE) quietNotification() else visibleNotification()"),
        )
        val visible = code.substringAfter("private fun visibleNotification()").substringBefore("private fun quietNotification()")
        assertTrue("visibleNotification was not found", visible.length < code.length / 2 && visible.isNotBlank())
        for (needed in listOf("CHANNEL_VISIBLE", "IMPORTANCE_LOW", "VISIBILITY_PUBLIC", "setSmallIcon")) {
            assertTrue("the Play build's running notice lost $needed", visible.contains(needed))
        }
        assertFalse("the Play build's running notice is still IMPORTANCE_MIN", visible.contains("IMPORTANCE_MIN"))
    }

    private fun build(): String = if (BuildConfig.PLAY_BUILD) "Play" else "self-hosted"
}
