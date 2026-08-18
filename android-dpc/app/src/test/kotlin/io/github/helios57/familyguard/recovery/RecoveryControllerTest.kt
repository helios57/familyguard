package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.RecoveryMaterial
import io.github.helios57.familyguard.sync.ApplyOutcome
import java.util.Base64
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The whole of FR-12 on the device side: what happens when somebody types a code.
 *
 * Everything Android is behind the four collaborators [RecoveryController] takes, so this drives the
 * real [RecoveryLockout], [RecoveryMode] and [RecoveryJournal] over in-memory stores and fakes only
 * the two things a JVM cannot do — the PBKDF2 derivation and the device-policy calls.
 *
 * The ordering test is the one worth reading twice. The three side effects of a correct code are
 * journal, then mode, then release, and that order is chosen so that *every prefix of it is a safe
 * place to be killed*: dying after the journal leaves a device that is still enforcing and has an
 * attempt to report; after the mode, one that is not yet released but will be at the next sync,
 * because `Synchronizer` reads the flag on every path. The reverse order has a bad prefix — a phone
 * released with no record of why and no flag to end it — and it is bad in exactly the situation
 * recovery happens in, where the device is under memory pressure with the screen off.
 */
class RecoveryControllerTest {

    private var clock = NOW
    private val lockoutStore = InMemoryLockoutStore()
    private val modeStore = InMemoryRecoveryModeStore()
    private val journalStore = InMemoryRecoveryJournalStore()

    /** Every side effect, in the order it happened. The ordering test reads this; the others do not. */
    private val events = mutableListOf<String>()

    private var material: RecoveryMaterial? = usableMaterial()
    private var releaseOutcome = ApplyOutcome("released")

    private fun controller() = RecoveryController(
        material = { material },
        lockout = RecoveryLockout(lockoutStore) { clock },
        mode = RecoveryMode(RecordingModeStore(modeStore, events)) { clock },
        journal = RecoveryJournal(RecordingJournalStore(journalStore, events)) { },
        release = {
            events += "release"
            releaseOutcome
        },
        now = { clock },
        // The real verifier, with the derivation faked: the fold, the refusals and the comparison are
        // this controller's contract with the server, and stubbing the verifier would remove them.
        verifierFor = { RecoveryVerifier(it, ::fakeDerive) },
    )

    // ---- a device that could never accept a code ---------------------------------------------

    /**
     * Not "wrong code" — *no* code. A device still mid-enrollment has nothing to check against, and
     * saying "incorrect" would send a parent looking for a typo in a code that was never issued.
     */
    @Test
    fun `a device that has not enrolled says so instead of rejecting the code`() {
        material = null

        val result = controller().submit(CODE)

        assertTrue("expected Unavailable, got $result", result is RecoveryResult.Unavailable)
        assertFalse(controller().available())
        assertNothingHappened()
    }

    @Test
    fun `a device enrolled without recovery material says so instead of rejecting the code`() {
        material = RecoveryMaterial(salt = "", iterations = 0, hash = "")

        val result = controller().submit(CODE)

        assertTrue("expected Unavailable, got $result", result is RecoveryResult.Unavailable)
        assertFalse(controller().available())
        assertNothingHappened()
    }

    @Test
    fun `a device with material the server issued reports recovery as available`() {
        assertTrue(controller().available())
    }

    // ---- the wrong code ----------------------------------------------------------------------

    @Test
    fun `a wrong code is rejected, recorded, and leaves the phone managed`() {
        val result = controller().submit(WRONG_CODE) as RecoveryResult.Rejected

        assertEquals(1, result.consecutiveFailures)
        assertEquals("the first mistake imposed a wait", LockoutStatus.Open, result.wait)
        assertEquals(
            listOf(RecoveryAttempt(succeeded = false, occurredAtEpochMillis = NOW)),
            journalStore.load(),
        )
        assertNull("a wrong code released the device", modeStore.activeSince())
        assertFalse("a wrong code reached the appliers", events.contains("release"))
    }

    @Test
    fun `the wait earned by a wrong code is what the caller is handed`() {
        val controller = controller()
        repeat(RecoveryLockout.FREE_ATTEMPTS) { controller.submit(WRONG_CODE) }

        val result = controller.submit(WRONG_CODE) as RecoveryResult.Rejected

        assertEquals(RecoveryLockout.FREE_ATTEMPTS + 1, result.consecutiveFailures)
        assertEquals(
            LockoutStatus.Closed(RecoveryLockout.ESCALATION.first()),
            result.wait,
        )
    }

    /**
     * A submission made while locked out is not an attempt at the code — nothing was verified.
     *
     * So it is not journalled and it does not escalate. A child holding the button down would
     * otherwise fill the pending queue with events that say nothing the lockout has not already
     * recorded, and push the real attempts out of a bounded journal.
     */
    @Test
    fun `a submission made while locked out checks nothing and records nothing`() {
        val controller = controller()
        repeat(RecoveryLockout.FREE_ATTEMPTS + 1) { controller.submit(WRONG_CODE) }
        val journalled = journalStore.load().size
        val failures = RecoveryLockout(lockoutStore) { clock }.consecutiveFailures()

        // The right code, submitted during the wait. It must not be checked either: a lockout that
        // could be probed with a correct code is a lockout that leaks whether the code was correct.
        val result = controller.submit(CODE) as RecoveryResult.TooManyAttempts

        assertTrue(result.remainingMillis > 0)
        assertEquals("a locked-out submission was journalled", journalled, journalStore.load().size)
        assertEquals(
            "a locked-out submission escalated the lockout",
            failures,
            RecoveryLockout(lockoutStore) { clock }.consecutiveFailures(),
        )
        assertNull("a locked-out submission released the device", modeStore.activeSince())
        assertFalse(events.contains("release"))
    }

    /** The wait is real: once it has been served, the same code is checked again. */
    @Test
    fun `once the wait has been served the code is checked again`() {
        val controller = controller()
        repeat(RecoveryLockout.FREE_ATTEMPTS + 1) { controller.submit(WRONG_CODE) }
        clock += RecoveryLockout.ESCALATION.first()

        val result = controller.submit(CODE)

        assertTrue("expected Released, got $result", result is RecoveryResult.Released)
    }

    // ---- the right code ----------------------------------------------------------------------

    @Test
    fun `the right code releases the device and records the attempt`() {
        val result = controller().submit(CODE) as RecoveryResult.Released

        assertEquals(releaseOutcome, result.outcome)
        assertEquals(NOW, modeStore.activeSince())
        assertEquals(
            listOf(RecoveryAttempt(succeeded = true, occurredAtEpochMillis = NOW)),
            journalStore.load(),
        )
    }

    /** Formatting is not a wrong code. The parent is copying twenty characters off another screen. */
    @Test
    fun `the code is accepted as the parent types it, in groups and in lower case`() {
        val result = controller().submit("  2345-6789-abcd-efgh-jkmn \n")

        assertTrue("expected Released, got $result", result is RecoveryResult.Released)
    }

    /**
     * Journal, then mode, then release. See the class KDoc: this order is what makes every prefix a
     * safe place to be killed, and no assertion about the end state can tell the orders apart.
     */
    @Test
    fun `the attempt is recorded, then the flag set, then the phone released`() {
        controller().submit(CODE)

        assertEquals(listOf("journal", "mode", "release"), events)
    }

    /**
     * A release that the appliers only half managed is still a release, and the problems are handed
     * back rather than swallowed.
     *
     * The alternative — treating a failed apply as a failed recovery — would leave the flag set and
     * the parent told nothing happened, which is the one report that makes the next step unguessable.
     */
    @Test
    fun `problems reported by the appliers are handed back, not hidden`() {
        releaseOutcome = ApplyOutcome("released", problems = mapOf("dns" to "not permitted on this OEM"))

        val result = controller().submit(CODE) as RecoveryResult.Released

        assertFalse(result.outcome.ok)
        assertEquals(mapOf("dns" to "not permitted on this OEM"), result.outcome.problems)
        assertEquals("a partial apply left the device unmarked", NOW, modeStore.activeSince())
    }

    @Test
    fun `a correct code clears the failures that came before it`() {
        val controller = controller()
        repeat(RecoveryLockout.FREE_ATTEMPTS) { controller.submit(WRONG_CODE) }

        controller.submit(CODE)

        assertEquals(0, RecoveryLockout(lockoutStore) { clock }.consecutiveFailures())
        assertEquals(LockoutStatus.Open, controller.status())
    }

    /**
     * A second code on an already-released device releases again and does not move the start.
     *
     * It happens: a parent who is not sure the first one worked types it again. The start is what the
     * console reports as "unmanaged since", so re-stamping it would quietly reset the one number
     * telling them how long the phone has been out of management.
     */
    @Test
    fun `a second correct code re-releases without moving the moment it started`() {
        val controller = controller()
        controller.submit(CODE)
        clock += 60 * 60 * 1000

        val result = controller.submit(CODE)

        assertTrue("expected Released, got $result", result is RecoveryResult.Released)
        assertTrue(controller.released())
        assertEquals(NOW, modeStore.activeSince())
        assertEquals(2, events.count { it == "release" })
    }

    private fun assertNothingHappened() {
        assertEquals("something happened on a device that cannot recover: $events", emptyList<String>(), events)
        assertEquals(emptyList<RecoveryAttempt>(), journalStore.load())
        assertNull(modeStore.activeSince())
    }

    /** Wraps a store so the controller's writes appear in [events] in the order they were made. */
    private class RecordingModeStore(
        private val inner: RecoveryModeStore,
        private val events: MutableList<String>,
    ) : RecoveryModeStore {
        override fun activeSince(): Long? = inner.activeSince()
        override fun setActiveSince(epochMillis: Long?) {
            events += "mode"
            inner.setActiveSince(epochMillis)
        }
    }

    private class RecordingJournalStore(
        private val inner: RecoveryJournalStore,
        private val events: MutableList<String>,
    ) : RecoveryJournalStore {
        override fun load(): List<RecoveryAttempt> = inner.load()
        override fun save(attempts: List<RecoveryAttempt>) {
            events += "journal"
            inner.save(attempts)
        }
    }

    private companion object {
        const val NOW = 1_755_000_000_000L

        const val CODE = "23456789ABCDEFGHJKMN"
        const val WRONG_CODE = "23456789ABCDEFGHJKMP"

        val SALT_BYTES = ByteArray(16) { (it * 7 + 3).toByte() }
        val HASH_BYTES = ByteArray(32) { (it * 11 + 5).toByte() }

        /**
         * Stands in for PBKDF2: [CODE] derives to the stored hash and everything else does not.
         *
         * Deliberately not a real derivation. What this suite is about is the decisions around the
         * check — the order of the side effects, what a lockout suppresses, what is journalled — and
         * 120 000 rounds per case would make it slow enough that people stop running it. Whether the
         * derivation itself matches the server's is `RecoveryVectorsTest`'s subject, and whether the
         * verifier refuses the right things is `RecoveryVerifierTest`'s.
         */
        @Suppress("UNUSED_PARAMETER")
        fun fakeDerive(normalizedCode: String, salt: ByteArray, iterations: Int): ByteArray =
            if (normalizedCode == CODE) HASH_BYTES else ByteArray(32)

        fun usableMaterial() = RecoveryMaterial(
            salt = Base64.getUrlEncoder().withoutPadding().encodeToString(SALT_BYTES),
            iterations = 120_000,
            hash = Base64.getUrlEncoder().withoutPadding().encodeToString(HASH_BYTES),
        )
    }
}
