package io.github.helios57.familyguard

import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.status.StatusLabels
import io.github.helios57.familyguard.status.StatusLevel
import io.github.helios57.familyguard.status.deviceStatus
import io.github.helios57.familyguard.status.deviceStatusFacts
import io.github.helios57.familyguard.usage.UsageStatsForegroundReader
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import java.io.FileInputStream

/**
 * The product's central claim, measured on a device: **a phone that cannot see usage says so, and
 * never reports zero minutes.**
 *
 * Everything else asserts this from the JVM with a fake reader, which proves the composition is
 * right and proves nothing about the platform. `PACKAGE_USAGE_STATS` is an appop rather than a
 * runtime permission, and a revoked appop does not make `queryEvents` throw — it makes it return
 * nothing at all. So the failure this class exists for looks, at every layer above the appop check,
 * exactly like a child who did not touch their phone: quota never reached, console showing a quiet
 * day, nothing red anywhere.
 *
 * It toggles the grant rather than reading whatever the device happens to have, because a test that
 * only observes the current state measures the device it ran on and not the code. Both halves are
 * asserted in one method on purpose: a revoked-half that passes on its own is also what a reader
 * hard-wired to return null would produce, and that would be a guard reporting the right answer for
 * a reason that makes it blind forever.
 *
 * Method names are camel case, not the backticked sentences the JVM suites use: this app targets
 * `minSdk 29`, and DEX below version 040 refuses a `SimpleName` containing a space.
 */
@RunWith(AndroidJUnit4::class)
class UsageAccessTest {

    private lateinit var context: Context

    @Before
    fun grantUsageAccess() {
        context = InstrumentationRegistry.getInstrumentation().targetContext
        setUsageAccess(ALLOW)
    }

    /**
     * Puts the grant back however the test ended.
     *
     * A device left with usage access revoked measures zero screen time for every later run, which
     * is the exact fault this class is about — so leaving it revoked would poison the suite with the
     * bug it was written to catch.
     */
    @After
    fun restoreUsageAccess() {
        setUsageAccess(ALLOW)
        assertEquals(
            "usage access could not be restored; this device will now measure no screen time at all",
            ALLOW,
            modeOfUsageAccess(),
        )
    }

    @Test
    fun revokingUsageAccessMakesScreenTimeUnmeasuredRatherThanZero() {
        // Positive half first. Without it, a reader that always answered null would pass the half
        // that matters and the toggle would be proved to do nothing.
        assertEquals(ALLOW, modeOfUsageAccess())
        val granted = reader().spans(from(), now())
        assertNotNull(
            "with usage access granted the reader must return a list, even an empty one; " +
                "null here means it cannot tell granted from revoked and neither can this test",
            granted,
        )
        assertTrue(
            "with usage access granted, screen time must be a measured value",
            screenTimeLine().level != StatusLevel.NOT_MEASURED,
        )

        setUsageAccess(IGNORE)
        // Read the appop back from the system rather than trusting the write. `appops set` prints
        // nothing on success and nothing on failure.
        assertEquals(
            "the appop was not actually revoked, so nothing below measures anything",
            IGNORE,
            modeOfUsageAccess(),
        )

        assertNull(
            "a phone that cannot see usage must report not-measured, never an empty list: an empty " +
                "list is folded to zero minutes and a quota measured against zero is never reached",
            reader().spans(from(), now()),
        )
        val line = screenTimeLine()
        assertEquals(
            "the status screen reported \"${line.value}\" for a phone that cannot measure usage",
            StatusLevel.NOT_MEASURED,
            line.level,
        )
        assertTrue(
            "the not-measured line must say why; it said \"${line.value}\"",
            line.value.contains("usage access"),
        )
    }

    private fun reader() = UsageStatsForegroundReader(context)

    private fun screenTimeLine() =
        deviceStatus(deviceStatusFacts(context)).line(StatusLabels.SCREEN_TIME)

    private fun now() = System.currentTimeMillis()

    private fun from() = now() - 60_000L

    private fun setUsageAccess(mode: String) {
        shell("appops set ${context.packageName} $OP $mode")
    }

    /** `appops get` prints `GET_USAGE_STATS: allow; time=…`, or `No operations.` when never set. */
    private fun modeOfUsageAccess(): String {
        val printed = shell("appops get ${context.packageName} $OP")
        return Regex("$OP:\\s*(\\w+)").find(printed)?.groupValues?.get(1) ?: printed.trim()
    }

    private fun shell(command: String): String {
        val fd = InstrumentationRegistry.getInstrumentation().uiAutomation
            .executeShellCommand(command)
        return FileInputStream(fd.fileDescriptor).use { it.readBytes().decodeToString() }
    }

    private companion object {
        const val OP = "GET_USAGE_STATS"
        const val ALLOW = "allow"
        const val IGNORE = "ignore"
    }
}
