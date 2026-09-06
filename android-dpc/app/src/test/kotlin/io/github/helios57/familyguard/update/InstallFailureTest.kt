package io.github.helios57.familyguard.update

import android.content.pm.PackageInstaller
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Turning what the installer reported into something a parent can act on (FR-15.7).
 *
 * The case this exists for is the one that happened: a `STATUS_FAILURE_BLOCKED` whose cause is in
 * `EXTRA_OTHER_PACKAGE_NAME` and nowhere else. Three unrelated things — a device policy, a package
 * verifier, a core system package — arrive as the same integer, so a reader given only the integer
 * has to guess which, and the guess a parent makes about their own phone is "it is broken".
 *
 * The `PackageInstaller.STATUS_*` constants are `static final int`, so the compiler inlines them
 * and none of this needs a device.
 */
class InstallFailureTest {

    @Test
    fun `a success is not a reason`() {
        assertEquals("", installFailureReason(PackageInstaller.STATUS_SUCCESS, "", null))
        assertEquals(
            "a success with a message attached is still a success; returning text for it would " +
                "record a failure under a phone that updated correctly",
            "",
            installFailureReason(PackageInstaller.STATUS_SUCCESS, "installed", "com.example"),
        )
    }

    @Test
    fun `a verifier block names Play Protect rather than the package it lives in`() {
        for (host in listOf("com.google.android.gms", "com.android.vending")) {
            val reason = installFailureReason(PackageInstaller.STATUS_FAILURE_BLOCKED, "", host)
            assertTrue(
                "a block by $host did not name Play Protect: $reason",
                reason.contains("Play Protect"),
            )
            assertTrue(
                "the words a parent needs — that the app is not recognised — are missing: $reason",
                reason.contains("does not recognise"),
            )
        }
    }

    @Test
    fun `a block by anything else names that thing, and does not blame Play Protect`() {
        val reason = installFailureReason(
            PackageInstaller.STATUS_FAILURE_BLOCKED, "", "com.samsung.knox.securefolder",
        )
        assertTrue("the blocking package is not named: $reason", reason.contains("com.samsung.knox.securefolder"))
        assertFalse(
            "an unrelated blocker was reported as Play Protect, which sends the parent to the " +
                "wrong settings screen: $reason",
            reason.contains("Play Protect"),
        )
    }

    @Test
    fun `a block with no named blocker does not invent one`() {
        val reason = installFailureReason(PackageInstaller.STATUS_FAILURE_BLOCKED, "", null)
        assertFalse("a blocker was named that the platform did not name: $reason", reason.contains("Play Protect"))
        assertTrue("the block itself is not reported: $reason", reason.contains("blocked this install"))
        // The whitespace-only case is the same case. A platform that fills the extra with "" would
        // otherwise produce " blocked this install".
        assertEquals(reason, installFailureReason(PackageInstaller.STATUS_FAILURE_BLOCKED, "", "  "))
    }

    @Test
    fun `a request for a tap is reported as what it is`() {
        val reason = installFailureReason(PackageInstaller.STATUS_PENDING_USER_ACTION, "", null)
        assertTrue("the device-owner point is missing: $reason", reason.contains("device owner"))
        assertTrue("it does not say nothing was installed: $reason", reason.contains("nothing was installed"))
    }

    /**
     * Every failure the platform documents gets its own sentence.
     *
     * The negative control is the point: the fallback text must be reachable, or "no status hits
     * the fallback" would be true of a function that had no fallback and no branches either.
     */
    @Test
    fun `every documented failure has words of its own, and the fallback still works`() {
        val documented = mapOf(
            PackageInstaller.STATUS_FAILURE to "refused",
            PackageInstaller.STATUS_FAILURE_BLOCKED to "blocked",
            PackageInstaller.STATUS_FAILURE_ABORTED to "stopped",
            PackageInstaller.STATUS_FAILURE_INVALID to "invalid",
            PackageInstaller.STATUS_FAILURE_CONFLICT to "conflicts",
            PackageInstaller.STATUS_FAILURE_STORAGE to "room",
            PackageInstaller.STATUS_FAILURE_INCOMPATIBLE to "does not run",
            PackageInstaller.STATUS_FAILURE_TIMEOUT to "in time",
        )
        for ((status, word) in documented) {
            val reason = installFailureReason(status, "", null)
            assertTrue("status $status is not described: $reason", reason.contains(word))
            assertFalse(
                "status $status fell through to the fallback, so the platform's own distinction " +
                    "was thrown away: $reason",
                reason.contains("an install status this app does not recognise"),
            )
            assertTrue("status $status does not carry its number: $reason", reason.contains("(status $status)"))
        }
        // Calibration: the fallback is reachable, so the assertions above measure something.
        val strange = installFailureReason(9999, "", null)
        assertTrue(
            "an unknown status did not reach the fallback, so the checks above prove nothing: $strange",
            strange.contains("an install status this app does not recognise"),
        )
        assertTrue("the unknown status is not quoted: $strange", strange.contains("(status 9999)"))
    }

    @Test
    fun `the platform's own words are kept, and absence of them is not reported as a word`() {
        val withMessage = installFailureReason(
            PackageInstaller.STATUS_FAILURE_INVALID, "  Failed to parse  ", null,
        )
        assertTrue("the platform's message was dropped: $withMessage", withMessage.contains("Failed to parse"))
        assertFalse(
            "the message was pasted in with its own whitespace: $withMessage",
            withMessage.contains("  Failed to parse"),
        )
        val without = installFailureReason(PackageInstaller.STATUS_FAILURE_INVALID, "", null)
        assertFalse(
            "an empty message produced a phrase that reads like the platform said something: $without",
            without.contains("Android said"),
        )
    }

    /**
     * The status number survives into the text.
     *
     * Not for the parent — for whoever is asked about it afterwards. A sentence without the number
     * cannot be matched back to `PackageInstaller`'s documentation, and this project has already
     * spent a day on a failure whose only trace was a line nobody could read.
     */
    @Test
    fun `every reason carries the number it came from`() {
        for (status in listOf(-1, 1, 2, 3, 4, 5, 6, 7, 8, Int.MIN_VALUE)) {
            val reason = installFailureReason(status, "", null)
            assertTrue("status $status is not quoted in: $reason", reason.contains("(status $status)"))
        }
    }
}
