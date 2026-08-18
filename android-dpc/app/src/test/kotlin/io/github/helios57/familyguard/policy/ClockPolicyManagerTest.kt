package io.github.helios57.familyguard.policy

import io.github.helios57.familyguard.enforce.EnforcementEngine
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A clock that can behave the way real ones do: comply, accept the call and change nothing, or
 * refuse. `internal` rather than private because the hardening and applier tests need one too, and
 * a second copy of this fake is a second thing that can drift from the interface it stands in for.
 */
internal class FakeClockGateway(
    private var enabled: Boolean = false,
    /** The OEM that returns from the setter having done nothing. This is the interesting one. */
    private val ignoreWrite: Boolean = false,
    private val throwOnRead: Boolean = false,
    private val throwOnWrite: Boolean = false,
    /** Refuses only the read-back, so the write half looks like it worked. */
    private val throwOnReadBack: Boolean = false,
    private val readMessage: String? = "this device does not report automatic time",
) : ClockGateway {
    val calls = mutableListOf<String>()
    private var reads = 0

    override fun autoTimeEnabled(): Boolean {
        calls += "read"
        reads++
        if (throwOnRead) throw SecurityException(readMessage)
        if (throwOnReadBack && reads > 1) throw IllegalStateException("the time service went away")
        return enabled
    }

    override fun enableAutoTime() {
        calls += "write"
        if (throwOnWrite) throw SecurityException("automatic time is controlled by the carrier")
        if (!ignoreWrite) enabled = true
    }
}

/** A clock that behaves, for the tests whose subject is something else. */
internal fun compliantClock() = ClockPolicyManager(FakeClockGateway(enabled = true))

class ClockPolicyManagerTest {

    @Test
    fun `it turns automatic time on when the device has it off`() {
        val gateway = FakeClockGateway(enabled = false)
        val outcome = ClockPolicyManager(gateway).apply()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("auto-time=on (was off)", outcome.summary)
        // Read, write, read: the last one is the whole point of the class.
        assertEquals(listOf("read", "write", "read"), gateway.calls)
    }

    @Test
    fun `it does not write when automatic time is already on`() {
        val gateway = FakeClockGateway(enabled = true)
        val outcome = ClockPolicyManager(gateway).apply()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("auto-time=on (unchanged)", outcome.summary)
        assertEquals(
            "a settings write on every boot for a value that already holds",
            listOf("read"),
            gateway.calls,
        )
    }

    /**
     * The failure this class exists for. `setAutoTimeEnabled` returns void, so an OEM that ignores
     * it is indistinguishable from one that complied — unless somebody reads the value back.
     */
    @Test
    fun `a device that accepts the call and stays off is a failure, not a pass`() {
        val gateway = FakeClockGateway(enabled = false, ignoreWrite = true)
        val outcome = ClockPolicyManager(gateway).apply()

        assertFalse(outcome.toString(), outcome.ok)
        assertEquals(listOf("read", "write", "read"), gateway.calls)
        val failure = requireNotNull(outcome.failure) { "expected a failure, got $outcome" }
        assertTrue(outcome.toString(), failure.contains("still off") && failure.contains("quota"))
    }

    @Test
    fun `a write that throws is reported, with what the platform said`() {
        val outcome = ClockPolicyManager(FakeClockGateway(throwOnWrite = true)).apply()

        assertFalse(outcome.toString(), outcome.ok)
        assertTrue(outcome.toString(), outcome.failure!!.contains("controlled by the carrier"))
    }

    /**
     * "Could not tell" is a failure, deliberately. A clock this app cannot read is a clock it
     * cannot trust, and a quota computed against an untrusted clock is not an enforced quota.
     */
    @Test
    fun `a clock that cannot be read is a failure, never a pass`() {
        val gateway = FakeClockGateway(throwOnRead = true)
        val outcome = ClockPolicyManager(gateway).apply()

        assertFalse(outcome.toString(), outcome.ok)
        assertEquals("it must not write blind", listOf("read"), gateway.calls)
        assertTrue(outcome.toString(), outcome.failure!!.contains("could not be read"))
    }

    @Test
    fun `a read-back that throws after a successful write is a failure`() {
        val outcome = ClockPolicyManager(FakeClockGateway(throwOnReadBack = true)).apply()

        assertFalse(outcome.toString(), outcome.ok)
        assertTrue(outcome.toString(), outcome.failure!!.contains("could not be read back"))
    }

    /** An exception with no message must still name something. A bare "failed" is not a report. */
    @Test
    fun `a refusal with no message is reported by its type`() {
        val outcome =
            ClockPolicyManager(FakeClockGateway(throwOnRead = true, readMessage = null)).apply()

        assertFalse(outcome.toString(), outcome.ok)
        assertTrue(outcome.toString(), outcome.failure!!.contains("SecurityException"))
    }

    @Test
    fun `the summary always names the clock, so a failure is not anonymous in a log line`() {
        val failed = ClockPolicyManager(FakeClockGateway(ignoreWrite = true)).apply()

        assertTrue(failed.toString(), failed.toString().startsWith("auto-time"))
        assertTrue(failed.toString(), failed.toString().contains("FAILED="))
    }
}

/**
 * FR-2.2 as the rest of the app sees it: the baseline asserts the clock, the sync path does not,
 * and a clock failure is enough on its own to make the baseline not-ok.
 */
class HardeningClockTest {

    private val safeBoot = EnforcementEngine.RESTRICTION_SAFE_BOOT

    @Test
    fun `the baseline enforces the clock`() {
        val clock = FakeClockGateway(enabled = false)
        val outcome = HardeningManager(FakeGateway(), ClockPolicyManager(clock)).applyBaseline()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("auto-time=on (was off)", outcome.clock!!.summary)
        assertEquals(listOf("read", "write", "read"), clock.calls)
    }

    /**
     * The whole point of folding it into the outcome rather than logging it separately: a phone
     * whose every restriction took and whose clock can still be set by hand is not a hardened
     * phone, and `ok` is what every caller checks.
     */
    @Test
    fun `a clock that will not be fixed fails the baseline, even with every restriction in effect`() {
        val restrictions = FakeGateway()
        val outcome = HardeningManager(
            restrictions,
            ClockPolicyManager(FakeClockGateway(ignoreWrite = true)),
        ).applyBaseline()

        assertFalse(outcome.toString(), outcome.ok)
        assertTrue(outcome.toString(), outcome.failures.isEmpty())
        assertTrue(outcome.toString(), outcome.missing.isEmpty())
        assertTrue(
            "the baseline restrictions did take; the clock is the only thing wrong",
            EnforcementEngine.BASELINE_RESTRICTIONS.all { it in restrictions.current() },
        )
        assertTrue(outcome.toString(), outcome.toString().contains("auto-time"))
    }

    /** The sync path must not touch it — see `HardeningManager.apply`. */
    @Test
    fun `applying a desired state leaves the clock alone and claims nothing about it`() {
        val clock = FakeClockGateway(enabled = false)
        val outcome =
            HardeningManager(FakeGateway(), ClockPolicyManager(clock)).apply(listOf(safeBoot))

        assertTrue(outcome.toString(), outcome.ok)
        assertNull("a sync must not report a clock it never looked at", outcome.clock)
        assertEquals(emptyList<String>(), clock.calls)
    }
}

/**
 * The one thing about FR-2.2 that no JVM test can execute: the API 29 / 30 split in
 * [DpmClockGateway], read out of the source because the two branches need a real phone of each
 * version to run.
 *
 * It is worth a source scan rather than nothing, because the failure it guards against is invisible.
 * `setAutoTimeRequired` compiles and runs on every supported version, so collapsing the split into
 * one call looks like a simplification and passes every other test in this repository — and on
 * Android 11 and later it means *"forbid changing automatic time"* rather than *"turn automatic time
 * on"*, which locks a phone that arrived with the setting off into exactly the state FR-2.2 exists
 * to prevent. The only evidence would be a child whose quota is never reached.
 */
class ClockGatewayVersionSplitTest {

    private val main: File =
        sequenceOf("src/main", "app/src/main", "android-dpc/app/src/main")
            .map(::File)
            .firstOrNull { it.isDirectory }
            ?: throw AssertionError(
                "could not find the main source set from ${File(".").absolutePath}; this " +
                    "test would otherwise pass by scanning nothing",
            )

    /** Comments removed, so the prose above about `setAutoTimeRequired` cannot satisfy the scan. */
    private val source: String =
        File(main, "kotlin/io/github/helios57/familyguard/policy/DpmClockGateway.kt").readText()
            .replace(Regex("""/\*.*?\*/""", RegexOption.DOT_MATCHES_ALL), "")
            .replace(Regex("""(?<!:)//[^\n]*"""), "")

    @Test
    fun `the scan read the gateway`() {
        assertTrue(
            "DpmClockGateway.kt did not resolve to a ClockGateway implementation; every " +
                "assertion below would pass by reading an empty string",
            source.contains("ClockGateway") && source.contains("override fun enableAutoTime"),
        )
    }

    @Test
    fun `both platform calls are present, each behind the version check that selects it`() {
        assertTrue(
            "the gateway no longer calls setAutoTimeEnabled, so on Android 11+ automatic time is " +
                "never turned on — only locked wherever it already was",
            source.contains("setAutoTimeEnabled"),
        )
        assertTrue(
            "the gateway no longer calls setAutoTimeRequired, which is the only way to reach the " +
                "setting on Android 10, and minSdk is 29 (NFR-13)",
            source.contains("setAutoTimeRequired"),
        )
        assertTrue(
            "the gateway no longer branches on the platform version, so one of its two calls is " +
                "reaching a version it does not exist on or does not mean what it says",
            source.contains("Build.VERSION_CODES.R"),
        )
        assertEquals(
            "each of the two calls is selected by its own version check: a read and a write",
            2,
            Regex("""SDK_INT\s*>=\s*Build\.VERSION_CODES\.R""").findAll(source).count(),
        )
    }

    @Test
    fun `the read side is split too, so a device is never asked a question its version cannot answer`() {
        assertTrue(
            "the gateway no longer reads getAutoTimeEnabled; on Android 11+ the deprecated getter " +
                "answers a different question — whether the setting is locked, not whether it is on",
            source.contains("getAutoTimeEnabled"),
        )
        assertTrue(
            "the gateway no longer reads the API 29 getter, so the read-back on Android 10 would " +
                "throw NoSuchMethodError at the moment it is most needed",
            source.contains("autoTimeRequired"),
        )
    }
}
