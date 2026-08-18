package io.github.helios57.familyguard.status

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What the phone tells whoever is holding it (FR-13.4).
 *
 * The screen is the only place a parent standing next to their child can find out why a rule is not
 * happening — the console can say what it *sent*, and nothing else. So the cases that matter here
 * are the ones where the phone knows something the console cannot: a policy that arrived and was
 * never applied, a release nobody reported yet, and above all a measurement that did not happen.
 *
 * These run on the JVM in milliseconds because [deviceStatus] is a pure function of [StatusFacts].
 * The alternative — asserting on a rendered `Activity` — is how a screen that reports a healthy
 * phone while measuring nothing stays green for a year.
 */
class DeviceStatusTest {

    // ---- the line this file exists for -------------------------------------------------------

    /**
     * Null screen time is NOT_MEASURED and says why, and is never rendered as a number.
     *
     * `0 minutes` and `cannot be measured` are the same pixels to a parent and opposite facts to the
     * product: without usage access the quota is never reached, bedtime is the only rule still
     * working, and the console shows a child who spent the day off their phone. There is no way to
     * tell that from the real thing except by this line.
     */
    @Test
    fun `screen time that could not be measured says so, and never says zero`() {
        val status = deviceStatus(facts(screenTimeTodayMillis = null))

        val line = status.line(StatusLabels.SCREEN_TIME)
        assertEquals(StatusLevel.NOT_MEASURED, line.level)
        assertEquals(UNAVAILABLE_REASON, line.value)
        assertFalse("an unmeasurable screen time rendered a number: ${line.value}", line.value.any { it.isDigit() })
    }

    /** And it is not quietly folded into OK by the summary either. */
    @Test
    fun `a phone that cannot measure screen time needs attention`() {
        val status = deviceStatus(facts(screenTimeTodayMillis = null))

        assertTrue(status.needsAttention())
        assertEquals(listOf(StatusLabels.SCREEN_TIME), status.problems().map { it.label })
    }

    @Test
    fun `measured screen time is shown against the limit`() {
        val status = deviceStatus(facts(screenTimeTodayMillis = 43L * 60 * 1000, quotaMinutes = 90))

        assertEquals(StatusLevel.OK, status.line(StatusLabels.SCREEN_TIME).level)
        assertEquals("43 of 90 minutes", status.line(StatusLabels.SCREEN_TIME).value)
    }

    /** A real zero is a fact, and reads as one. It is the *absence* that must not. */
    @Test
    fun `a genuine zero is measured, and reads differently from an unmeasurable one`() {
        val measured = deviceStatus(facts(screenTimeTodayMillis = 0, quotaMinutes = 90))
        val unmeasured = deviceStatus(facts(screenTimeTodayMillis = null, quotaMinutes = 90))

        assertEquals(StatusLevel.OK, measured.line(StatusLabels.SCREEN_TIME).level)
        assertEquals("0 of 90 minutes", measured.line(StatusLabels.SCREEN_TIME).value)
        assertTrue(
            "a measured zero and an unmeasurable one render the same",
            measured.line(StatusLabels.SCREEN_TIME) != unmeasured.line(StatusLabels.SCREEN_TIME),
        )
    }

    @Test
    fun `with no daily limit set, the minutes are shown without one`() {
        val status = deviceStatus(facts(screenTimeTodayMillis = 12L * 60 * 1000, quotaMinutes = 0))

        assertEquals("12 minutes", status.line(StatusLabels.SCREEN_TIME).value)
    }

    // ---- the gap only the phone can see ------------------------------------------------------

    /**
     * The server sent v9, this phone is applying v7.
     *
     * Every rule added between them is simply not happening, and the console cannot tell: it knows
     * it sent v9 and it knows the device is heartbeating. The phone knows both numbers.
     */
    @Test
    fun `a policy that arrived but was never applied is called out with both versions`() {
        val status = deviceStatus(facts(appliedPolicyVersion = 7, cachedPolicyVersion = 9))

        val line = status.line(StatusLabels.POLICY)
        assertEquals(StatusLevel.ATTENTION, line.level)
        assertTrue("the sent version is missing: ${line.value}", line.value.contains("9"))
        assertTrue("the applied version is missing: ${line.value}", line.value.contains("7"))
    }

    @Test
    fun `a phone applying what it was sent is fine`() {
        val status = deviceStatus(facts(appliedPolicyVersion = 9, cachedPolicyVersion = 9))

        assertEquals(StatusLevel.OK, status.line(StatusLabels.POLICY).level)
        assertEquals("version 9", status.line(StatusLabels.POLICY).value)
    }

    /**
     * A cached version *behind* the applied one is not a problem.
     *
     * It is what a device looks like between a successful apply and the next fetch on some paths,
     * and flagging it would put an attention banner on a healthy phone — which is how a banner stops
     * being read at all.
     */
    @Test
    fun `a cached version older than the applied one is not a problem`() {
        val status = deviceStatus(facts(appliedPolicyVersion = 9, cachedPolicyVersion = 8))

        assertEquals(StatusLevel.OK, status.line(StatusLabels.POLICY).level)
    }

    @Test
    fun `a phone that has never received settings says so`() {
        val status = deviceStatus(facts(appliedPolicyVersion = 0, cachedPolicyVersion = null))

        assertEquals(StatusLevel.ATTENTION, status.line(StatusLabels.POLICY).level)
    }

    // ---- reachability ------------------------------------------------------------------------

    @Test
    fun `a phone that has never reached the server says never, not just now`() {
        val status = deviceStatus(facts(lastServerContactMillis = 0))

        val line = status.line(StatusLabels.LAST_CONTACT)
        assertEquals("never", line.value)
        assertEquals(StatusLevel.ATTENTION, line.level)
    }

    @Test
    fun `a recent contact is fine, and reads as an age`() {
        val status = deviceStatus(facts(lastServerContactMillis = NOW - 20L * 60 * 1000))

        assertEquals(StatusLevel.OK, status.line(StatusLabels.LAST_CONTACT).level)
        assertEquals("20 minutes ago", status.line(StatusLabels.LAST_CONTACT).value)
    }

    /**
     * A day is the threshold, and it is inclusive.
     *
     * Not a failure — a phone enforcing yesterday's policy is still enforcing — but it is the first
     * thing to look at when a change the parent made has not happened, so it has to be visible
     * before somebody thinks to ask.
     */
    @Test
    fun `a phone out of contact for a day wants attention`() {
        val fresh = deviceStatus(facts(lastServerContactMillis = NOW - 24L * 60 * 60 * 1000 + 1))
        val stale = deviceStatus(facts(lastServerContactMillis = NOW - 24L * 60 * 60 * 1000))

        assertEquals(StatusLevel.OK, fresh.line(StatusLabels.LAST_CONTACT).level)
        assertEquals(StatusLevel.ATTENTION, stale.line(StatusLabels.LAST_CONTACT).level)
        assertEquals("1 day ago", stale.line(StatusLabels.LAST_CONTACT).value)
    }

    /**
     * A clock that moved forward and back leaves a contact in the future. It reads as "just now",
     * not as a negative age or a date next Tuesday.
     */
    @Test
    fun `a contact in the future reads as just now`() {
        val status = deviceStatus(facts(lastServerContactMillis = NOW + 60L * 60 * 1000))

        assertEquals("just now", status.line(StatusLabels.LAST_CONTACT).value)
        assertEquals(StatusLevel.OK, status.line(StatusLabels.LAST_CONTACT).level)
    }

    // ---- management and recovery -------------------------------------------------------------

    /**
     * "We asked and the answer was no" and "we could not ask" are different problems.
     *
     * The first is a phone that was never provisioned as a device owner and has to be factory reset
     * to become one; the second is a bug in this app. Collapsing the second into the first sends a
     * parent to wipe a phone over a bug.
     */
    @Test
    fun `device ownership that could not be determined is not the same as not owning`() {
        val unknown = deviceStatus(facts(deviceOwner = null)).line(StatusLabels.DEVICE_OWNER)
        val notOwner = deviceStatus(facts(deviceOwner = false)).line(StatusLabels.DEVICE_OWNER)

        assertEquals(StatusLevel.NOT_MEASURED, unknown.level)
        assertEquals(StatusLevel.ATTENTION, notOwner.level)
        assertTrue("the two render identically", unknown.value != notOwner.value)
    }

    @Test
    fun `a released phone says so, since when, and what ends it`() {
        val status = deviceStatus(facts(releasedSinceMillis = NOW - 3L * 60 * 60 * 1000))

        val line = status.line(StatusLabels.RULES)
        assertEquals(StatusLevel.ATTENTION, line.level)
        assertTrue("the age is missing: ${line.value}", line.value.contains("3 hours ago"))
        assertTrue(
            "the screen does not say what ends the release: ${line.value}",
            line.value.contains("reaches the family settings"),
        )
    }

    @Test
    fun `a managed phone says the rules are on`() {
        assertEquals(StatusLevel.OK, deviceStatus(facts()).line(StatusLabels.RULES).level)
    }

    /**
     * Recovery attempts the phone has not been able to report are shown here and nowhere else.
     *
     * They are queued on the device precisely because the network was not available, so the console
     * has not heard about them — a child working through guesses on a phone in flight mode is
     * invisible everywhere except this line.
     */
    @Test
    fun `unreported recovery attempts are shown, and are absent when there are none`() {
        val quiet = deviceStatus(facts(unreportedRecoveryAttempts = 0))
        val busy = deviceStatus(facts(unreportedRecoveryAttempts = 4))

        assertTrue(quiet.lines.none { it.label == StatusLabels.UNREPORTED })
        assertEquals(StatusLevel.ATTENTION, busy.line(StatusLabels.UNREPORTED).level)
        assertTrue(busy.line(StatusLabels.UNREPORTED).value.contains("4 recovery attempts"))
    }

    @Test
    fun `one unreported attempt is not four attempts with a one in front of them`() {
        val status = deviceStatus(facts(unreportedRecoveryAttempts = 1))

        assertTrue(status.line(StatusLabels.UNREPORTED).value.contains("1 recovery attempt "))
    }

    // ---- enrollment, and what must never be on the screen --------------------------------------

    @Test
    fun `an un-enrolled phone says how to enroll it`() {
        val status = deviceStatus(facts(deviceId = null, serverHost = null))

        val line = status.line(StatusLabels.ENROLLMENT)
        assertEquals(StatusLevel.ATTENTION, line.level)
        assertTrue("no instruction: ${line.value}", line.value.contains("QR code"))
    }

    @Test
    fun `an enrolled phone shows the id and host, so a parent can match it to the console`() {
        val status = deviceStatus(facts())

        val line = status.line(StatusLabels.ENROLLMENT)
        assertEquals(StatusLevel.OK, line.level)
        assertTrue(line.value.contains("dev-4417"))
        assertTrue(line.value.contains("mdm.example.ch"))
    }

    /**
     * **The device token is never on this screen.**
     *
     * The status screen is the launcher entry (see `RecoveryActivity`), so anyone holding the phone
     * can read it — the child it is managing, most of all. The device id is fine and is needed to
     * match the phone to the console; the token authenticates as this device, and anyone who read it
     * off the screen could report fake usage, pull the family's policy, and heartbeat as a phone
     * sitting in a drawer.
     *
     * Asserted here rather than trusted, because the natural next line somebody adds is "Token: …"
     * while debugging a sync problem, and it is exactly as easy to leave in.
     */
    @Test
    fun `no line carries the device token`() {
        val status = deviceStatus(
            facts(unreportedRecoveryAttempts = 3, screenTimeTodayMillis = null, deviceOwner = null)
        )

        val rendered = status.lines.joinToString("\n") { "${it.label}: ${it.value}" }
        assertFalse(
            "the device token reached the status screen",
            rendered.contains(TOKEN),
        )
        // A negative control: this assertion can only be trusted if the same search finds something
        // that IS on the screen. Without it, a change that renders no lines at all passes.
        assertTrue("the search found nothing at all, so it proves nothing", rendered.contains("dev-4417"))
    }

    /** A healthy phone has no problems and no banner. The base fixture is that phone. */
    @Test
    fun `the healthy phone is quiet`() {
        val status = deviceStatus(facts())

        assertEquals(emptyList<StatusLine>(), status.problems())
        assertFalse(status.needsAttention())
    }

    /** Every line is labelled, and no label appears twice — the screen is read top to bottom. */
    @Test
    fun `the labels are unique`() {
        val labels = deviceStatus(facts(unreportedRecoveryAttempts = 1)).lines.map { it.label }

        assertEquals(labels.size, labels.toSet().size)
        assertTrue(labels.none { it.isBlank() })
    }

    // ---- ages --------------------------------------------------------------------------------

    /**
     * The boundaries, as literals.
     *
     * Stated here rather than derived, for the reason `RecoveryLockoutTest` had to learn: a test that
     * reaches the thresholds through the same constants the code uses re-reads whatever they become
     * and agrees with it.
     */
    @Test
    fun `ages round down, in the largest unit that is still honest`() {
        assertEquals("just now", ago(0))
        assertEquals("just now", ago(59_000))
        assertEquals("1 minute ago", ago(60_000))
        assertEquals("59 minutes ago", ago(3_599_000))
        assertEquals("1 hour ago", ago(3_600_000))
        assertEquals("1 hour ago", ago(5_400_000))
        assertEquals("23 hours ago", ago(86_399_000))
        assertEquals("1 day ago", ago(86_400_000))
        assertEquals("8 days ago", ago(8L * 86_400_000))
    }

    /** The screen must never say "no such line" by accident — asking for one is a programming error. */
    @Test(expected = NoSuchElementException::class)
    fun `asking for a line that is not on the screen is an error, not an empty string`() {
        deviceStatus(facts()).line("Battery")
    }

    // ---- fixtures ----------------------------------------------------------------------------

    /** A healthy, enrolled, managed, measuring phone. Each test changes exactly what it is about. */
    private fun facts(
        deviceId: String? = "dev-4417",
        serverHost: String? = "mdm.example.ch",
        deviceOwner: Boolean? = true,
        releasedSinceMillis: Long? = null,
        appliedPolicyVersion: Long = 9,
        cachedPolicyVersion: Long? = 9,
        lastServerContactMillis: Long = NOW - 5L * 60 * 1000,
        screenTimeTodayMillis: Long? = 30L * 60 * 1000,
        quotaMinutes: Int = 90,
        unreportedRecoveryAttempts: Int = 0,
    ) = StatusFacts(
        deviceId = deviceId,
        serverHost = serverHost,
        deviceOwner = deviceOwner,
        releasedSinceMillis = releasedSinceMillis,
        appliedPolicyVersion = appliedPolicyVersion,
        cachedPolicyVersion = cachedPolicyVersion,
        lastServerContactMillis = lastServerContactMillis,
        screenTimeTodayMillis = screenTimeTodayMillis,
        screenTimeUnavailableReason = UNAVAILABLE_REASON,
        quotaMinutes = quotaMinutes,
        unreportedRecoveryAttempts = unreportedRecoveryAttempts,
        nowMillis = NOW,
    )

    private companion object {
        const val NOW = 1_786_989_600_000L

        const val UNAVAILABLE_REASON =
            "not measured — usage access is off for this app, so the daily limit cannot work"

        /**
         * Not a field of [StatusFacts] at all, which is half the guard: a token cannot reach the
         * screen through a channel that does not exist. The other half is that a future change
         * adding one has a test that fails.
         *
         * Built rather than written out as a literal. What the assertion needs is a token-SHAPED
         * value of the right length; what it never needed is entropy — and a 24-character
         * random-looking constant is a finding for every secret scanner there is. Generating it
         * removes the only property that ever made it look like a credential, which is why this
         * repository carries no scanner allowlist at all.
         */
        val TOKEN = "fgt_" + "abcdefghijklmnopqrstuvwx".take(20)
    }
}
