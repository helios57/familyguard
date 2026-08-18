package io.github.helios57.familyguard.commands

import io.github.helios57.familyguard.net.DeviceCommand
import io.github.helios57.familyguard.net.LocationRequest
import io.github.helios57.familyguard.policy.LockGateway
import io.github.helios57.familyguard.policy.LockManager
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

private class StubLockGateway(
    private val secure: Boolean = true,
    private var locked: Boolean = false,
) : LockGateway {
    var lockCalls = 0
        private set

    override fun lockNow() {
        lockCalls++
        locked = true
    }

    override fun deviceSecure(): Boolean = secure

    override fun deviceLocked(): Boolean = locked
}

private class StubSirenDevice : SirenDevice {
    var ringing = false
        private set

    override fun startTone() {
        ringing = true
    }

    override fun stopTone() {
        ringing = false
    }

    override fun startVibration() = Unit

    override fun stopVibration() = Unit

    override fun alarmVolume(): Int = 3

    override fun maxAlarmVolume(): Int = 7

    override fun setAlarmVolume(level: Int) = Unit
}

private class StubTimer : SirenTimer {
    override fun arm(delayMillis: Long, action: () -> Unit) = Unit
    override fun cancel() = Unit
}

private class StubLocationSource(
    private val permitted: Boolean = true,
    private val fix: Fix? = null,
) : LocationSource {
    override fun permitted(): Boolean = permitted
    override fun freshFix(timeoutMillis: Long): Fix? = fix
    override fun lastKnownFix(): Fix? = null
}

private const val NOW = 1_700_000_000_000L

/**
 * @param resyncProblem what the re-sync reports; null is success.
 * @param reportFails whether delivering a position throws, which on a real phone is a link that
 * dropped between getting the fix and sending it.
 */
private class Harness(
    secure: Boolean = true,
    locked: Boolean = false,
    fix: Fix? = null,
    permitted: Boolean = true,
    private val resyncProblem: String? = null,
    private val reportFails: Boolean = false,
) {
    val lockGateway = StubLockGateway(secure, locked)
    val sirenDevice = StubSirenDevice()
    val reported = mutableListOf<LocationRequest>()
    var resyncs = 0
        private set

    val handlers: Map<String, CommandHandler> = CommandHandlers(
        lock = LockManager(lockGateway),
        siren = SirenController(sirenDevice, StubTimer()),
        location = LocationProbe(StubLocationSource(permitted, fix), { NOW }),
        reportLocation = {
            if (reportFails) throw IllegalStateException("no route to host")
            reported += it
        },
        resync = {
            resyncs++
            resyncProblem
        },
        now = { NOW },
    ).asMap()

    fun run(type: String): CommandOutcome =
        handlers.getValue(type).handle(DeviceCommand(id = "id", type = type))
}

class CommandHandlersTest {

    /**
     * The set of types the device implements is the set the server will queue.
     *
     * Read out of the Go source rather than restated here, because a list copied into a Kotlin test
     * is a list that agrees with itself. This is the real failure it guards: the server is deployed
     * before the fleet updates, a parent presses a button for a command no phone implements, and the
     * console shows a queued row that is answered — correctly, by the executor — with "this device
     * does not implement it". The guard turns that into a red build at the moment the type is added.
     */
    @Test
    fun `the handlers implement exactly the command types the server accepts`() {
        val models = sequenceOf(
            "../backend/internal/store/models.go",
            "backend/internal/store/models.go",
            "../../backend/internal/store/models.go",
            "../../../backend/internal/store/models.go",
            "../../../../backend/internal/store/models.go",
        ).map(::File).firstOrNull { it.isFile }
            ?: throw AssertionError(
                "could not find backend/internal/store/models.go from ${File(".").absolutePath}; " +
                    "this test would otherwise pass by comparing against nothing",
            )

        // The `CmdType… = "…"` constants, then the ones `ValidCommandTypes` actually admits. Both
        // halves are needed: a constant that exists but is not in the map is a type the API rejects.
        val text = models.readText()
        val constants = Regex("""CmdType(\w+)\s*=\s*"([A-Z_]+)"""")
            .findAll(text)
            .associate { it.groupValues[1] to it.groupValues[2] }
        assertTrue(
            "no CmdType constants were found in ${models.path}; the parser is reading the wrong shape",
            constants.isNotEmpty(),
        )

        val mapBody = Regex("""ValidCommandTypes\s*=\s*map\[string]bool\{(.*?)}""", RegexOption.DOT_MATCHES_ALL)
            .find(text)?.groupValues?.get(1)
            ?: throw AssertionError("ValidCommandTypes was not found in ${models.path}")
        val accepted = Regex("""CmdType(\w+)\s*:\s*true""")
            .findAll(mapBody)
            .mapNotNull { constants[it.groupValues[1]] }
            .toSet()
        assertTrue("ValidCommandTypes parsed as empty", accepted.isNotEmpty())

        val implemented = Harness().handlers.keys
        assertEquals(
            "the device's command handlers and the server's accepted types have diverged; " +
                "unimplemented: ${accepted - implemented}, not accepted by the server: " +
                "${implemented - accepted}",
            accepted,
            implemented,
        )
    }

    @Test
    fun `LOCK_NOW locks the phone`() {
        val harness = Harness(secure = true, locked = false)

        val outcome = harness.run(CommandHandlers.LOCK_NOW)

        assertTrue(outcome is CommandOutcome.Done)
        assertEquals(1, harness.lockGateway.lockCalls)
    }

    @Test
    fun `LOCK_NOW on a phone with no PIN reports the failure rather than a lock that does not hold`() {
        val harness = Harness(secure = false)

        val outcome = harness.run(CommandHandlers.LOCK_NOW)

        val failed = outcome as CommandOutcome.Failed
        assertTrue(failed.reason, failed.reason.contains("swipe"))
    }

    @Test
    fun `UNLOCK_DEVICE re-syncs and says the device stops re-locking`() {
        val harness = Harness()

        val outcome = harness.run(CommandHandlers.UNLOCK_DEVICE)

        // There is no platform call that dismisses a keyguard. Acknowledging an unlock that never
        // happened would be the one thing worse than this honest wording.
        val done = outcome as CommandOutcome.Done
        assertEquals(1, harness.resyncs)
        assertTrue(done.result.getValue("state"), done.result.getValue("state").contains("re-locking"))
    }

    @Test
    fun `TRIGGER_ALARM rings and STOP_ALARM silences it`() {
        val harness = Harness()

        assertTrue(harness.run(CommandHandlers.TRIGGER_ALARM) is CommandOutcome.Done)
        assertTrue(harness.sirenDevice.ringing)

        assertTrue(harness.run(CommandHandlers.STOP_ALARM) is CommandOutcome.Done)
        assertFalse(harness.sirenDevice.ringing)
    }

    @Test
    fun `LOCATE_NOW delivers the position before it acknowledges the command`() {
        val harness = Harness(fix = Fix(47.37, 8.54, 12.0, NOW))

        val outcome = harness.run(CommandHandlers.LOCATE_NOW)

        val done = outcome as CommandOutcome.Done
        assertEquals(1, harness.reported.size)
        assertEquals(47.37, harness.reported.single().latitude, 0.0)
        assertEquals("2023-11-14T22:13:20Z", harness.reported.single().capturedAt)
        assertEquals("gnss", done.result["source"])
        assertEquals("0", done.result["age_seconds"])
        // No coordinates in the acknowledgement: the position belongs in the row the console draws a
        // map from, and duplicating it into a free-text result is two sources for one fact.
        assertFalse(done.result.values.any { it.contains("47.37") })
    }

    @Test
    fun `a position that cannot be delivered is a failed command, not a successful one`() {
        val harness = Harness(fix = Fix(47.37, 8.54, 12.0, NOW), reportFails = true)

        val outcome = harness.run(CommandHandlers.LOCATE_NOW)

        // The parent asked to be shown where the phone is. A "done" whose position never arrived is
        // a console that shows nothing and reports success.
        val failed = outcome as CommandOutcome.Failed
        assertTrue(failed.reason, failed.reason.contains("could not be delivered"))
    }

    @Test
    fun `LOCATE_NOW without permission fails with something a parent can act on`() {
        val harness = Harness(permitted = false)

        val outcome = harness.run(CommandHandlers.LOCATE_NOW)

        val failed = outcome as CommandOutcome.Failed
        assertTrue(failed.reason, failed.reason.contains("permission"))
        assertTrue(harness.reported.isEmpty())
    }

    @Test
    fun `the four state commands re-sync, and each of them fails when the sync did`() {
        val stateCommands = listOf(
            CommandHandlers.UNLOCK_DEVICE,
            CommandHandlers.BLOCK_YOUTUBE_ALL,
            CommandHandlers.UNBLOCK_YOUTUBE_ALL,
            CommandHandlers.SYNC_POLICY,
        )

        for (type in stateCommands) {
            val ok = Harness()
            assertTrue("$type on a good sync", ok.run(type) is CommandOutcome.Done)
            assertEquals("$type re-syncs", 1, ok.resyncs)

            // A sync served from the cache has applied *something* and still failed at the thing the
            // parent asked for, and reporting "policy re-fetched" for it is a false green with a
            // person reading it.
            val broken = Harness(resyncProblem = "the server could not be reached")
            val outcome = broken.run(type)
            assertTrue("$type on a failed sync", outcome is CommandOutcome.Failed)
            assertEquals(
                "the server could not be reached",
                (outcome as CommandOutcome.Failed).reason,
            )
        }
    }

    @Test
    fun `an absent note is absent rather than empty`() {
        val harness = Harness(secure = true, locked = false)

        val done = harness.run(CommandHandlers.LOCK_NOW) as CommandOutcome.Done

        // An empty-string value in the result map shows up in the console as a blank line under the
        // command, which reads as a truncated message rather than as nothing to say.
        assertFalse(done.result.containsKey("note"))
        assertTrue(done.result.values.none { it.isBlank() })
    }
}
