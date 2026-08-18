package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.App
import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.enforce.Settings
import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.HttpResponse
import io.github.helios57.familyguard.net.LoopbackServer
import io.github.helios57.familyguard.recovery.InMemoryRecoveryModeStore
import io.github.helios57.familyguard.recovery.RecoveryMode
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What one sync does, and — more to the point — what it does when something goes wrong.
 *
 * Every failure path here has the same worst outcome: a phone that stops enforcing. The tests are
 * written to name that outcome rather than the mechanism, so that a rewrite of [Synchronizer] which
 * keeps the behaviour keeps them green and one that quietly clears the policy on a failed fetch does
 * not.
 *
 * A real socket rather than a stubbed [ApiClient]: the difference between "the server refused" and
 * "the server could not be reached" is the whole subject of half these cases, and it is a property
 * of HTTP, not of this class's opinion of it.
 */
class SynchronizerTest {

    /**
     * The default answer to both device calls.
     *
     * The heartbeat's `policy_version` is 99 — a number no test ever expects to see stored. It is
     * there because [Synchronizer] deliberately does not read that field back into the cache:
     * believing it would close the loop on the device's own claim, and 99 is what would show up if
     * it ever started.
     */
    private val server = LoopbackServer { request ->
        if (request.path.endsWith("/heartbeat")) {
            HttpResponse(200, body = """{"policy_version":99,"pending_commands":2}""")
        } else {
            HttpResponse(200, body = policyBody())
        }
    }
    private val api = ApiClient(server.baseUrl, token = { "device-token" })
    private val cache = FakeCache()
    private val applier = RecordingApplier()
    private val recoveryStore = InMemoryRecoveryModeStore()
    private val recovery = RecoveryMode(recoveryStore) { RECOVERED_AT }

    @After
    fun tearDown() = server.close()

    private fun synchronizer(
        now: String = DEVICE_NOW,
        localUsedMinutes: (Input) -> Int = { 0 },
    ) = Synchronizer(
        api,
        cache,
        applier,
        recovery,
        telemetry = { TELEMETRY },
        now = { now },
        localUsedMinutes = localUsedMinutes,
    )

    // ---- the happy path ---------------------------------------------------------------------

    @Test
    fun `a fetched policy is applied, and its version is claimed`() {
        val result = synchronizer().sync() as SyncResult.Applied

        assertEquals(PolicySource.SERVER, result.source)
        assertEquals(7L, result.state.policyVersion)
        assertTrue("the apply reported problems: ${result.outcome}", result.outcome.ok)
        assertEquals(2, result.pendingCommands)
        assertEquals(1, applier.applied.size)
        assertEquals(7L, cache.applied)
    }

    /**
     * The input is written before the appliers run, so a process killed mid-apply comes back to the
     * new policy rather than to the one before it. Ordering is the whole assertion: both a
     * save-first and a save-last implementation end with the same stored value.
     */
    @Test
    fun `the fetched input is cached before it is applied`() {
        cache.onSave = { events += "save" }
        applier.onApply = { events += "apply" }

        synchronizer().sync()

        assertEquals(listOf("save", "apply"), events)
    }

    // ---- offline ----------------------------------------------------------------------------

    /** FR-9. No network, and the phone keeps enforcing the last thing the parent set. */
    @Test
    fun `a fetch that fails falls back to the cached policy`() {
        cache.stored = inputAt("2026-08-17T09:00:00+02:00", version = 4)
        server.stopAnswering()

        val result = synchronizer().sync() as SyncResult.Applied

        assertEquals(PolicySource.CACHE, result.source)
        assertEquals(4L, result.state.policyVersion)
        assertEquals(1, applier.applied.size)
    }

    /**
     * The one state in which nothing can be enforced — and the appliers must not run.
     *
     * An empty [DesiredState] handed to [RestrictionApplier] clears every managed restriction, so a
     * `Deferred` that still called the appliers would mean "the server did not answer" unlocked the
     * phone. That is the single most damaging bug this class could have, and it is invisible: the
     * device reports a clean sync and the child finds an open phone.
     */
    @Test
    fun `nothing fetched and nothing cached applies nothing`() {
        server.stopAnswering()

        val result = synchronizer().sync()

        assertTrue("expected Deferred, got $result", result is SyncResult.Deferred)
        assertTrue("the appliers ran with no policy at all", applier.applied.isEmpty())
    }

    // ---- refusals ---------------------------------------------------------------------------

    /**
     * A revoked credential. Waiting cannot fix it, so it is reported as such — and, again, without
     * touching the device: a parent who deletes a phone from the console has not asked for it to be
     * unlocked, and this path used to be the difference between the two.
     */
    @Test
    fun `a revoked credential refuses, and applies nothing`() {
        cache.stored = inputAt(SERVER_NOW, version = 4)
        server.answerWith { HttpResponse(401, reason = "Unauthorized", body = """{"error":"unauthorized"}""") }

        val result = synchronizer().sync()

        assertTrue("expected Refused, got $result", result is SyncResult.Refused)
        assertEquals(401, (result as SyncResult.Refused).cause.status)
        assertTrue("the appliers ran for a device the server has revoked", applier.applied.isEmpty())
    }

    /** A backend restarting is not a revoked credential: the cached policy still applies. */
    @Test
    fun `a server error falls back to the cache rather than refusing`() {
        cache.stored = inputAt(SERVER_NOW, version = 4)
        server.answerWith { HttpResponse(503, reason = "Service Unavailable", body = """{"error":"down"}""") }

        val result = synchronizer().sync()

        assertTrue("expected Applied, got $result", result is SyncResult.Applied)
        assertEquals(PolicySource.CACHE, (result as SyncResult.Applied).source)
    }

    /**
     * A policy that cannot be evaluated — an unknown timezone from a console typo. Refusing beats
     * guessing: a default state is an unlocked phone with nothing anywhere reporting a problem.
     */
    @Test
    fun `an unusable policy is rejected, and applies nothing`() {
        server.answerWith {
            HttpResponse(200, body = policyBody(input = inputAt(SERVER_NOW).let {
                it.copy(settings = it.settings.copy(timezone = "Middle/Earth"))
            }))
        }

        val result = synchronizer().sync()

        assertTrue("expected Rejected, got $result", result is SyncResult.Rejected)
        assertTrue("the appliers ran on a policy that does not evaluate", applier.applied.isEmpty())
    }

    // ---- the clock --------------------------------------------------------------------------

    /**
     * The engine runs against *this device's* clock, never the instant in the policy.
     *
     * The server's `now` is 20:00, an hour before bedtime; the device's is 22:00, an hour into it.
     * Reading the server's would mean a phone that fetched before bedtime never started it — the
     * exact failure a cached policy is supposed to prevent, arriving through the online path.
     */
    @Test
    fun `the desired state is computed from the device clock, not the server's`() {
        val beforeBedtime = synchronizer(now = "2026-08-17T20:00:00+02:00").sync() as SyncResult.Applied
        assertEquals(EnforcementEngine.REASON_NONE, beforeBedtime.state.suspendReason)

        val duringBedtime = synchronizer(now = "2026-08-17T22:00:00+02:00").sync() as SyncResult.Applied
        assertEquals(EnforcementEngine.REASON_BEDTIME, duringBedtime.state.suspendReason)
        assertTrue("bedtime did not suspend anything", duringBedtime.state.suspendedPackages.isNotEmpty())
    }

    /** The same input, re-evaluated with no network at all: the bedtime alarm's path (NFR-10). */
    @Test
    fun `enforcing from cache needs no server`() {
        cache.stored = inputAt(SERVER_NOW, version = 4)
        server.stopAnswering()

        val result = synchronizer(now = "2026-08-17T22:00:00+02:00").enforceFromCache() as SyncResult.Applied

        assertEquals(PolicySource.CACHE, result.source)
        assertEquals(EnforcementEngine.REASON_BEDTIME, result.state.suspendReason)
    }

    @Test
    fun `enforcing from an empty cache applies nothing`() {
        val result = synchronizer().enforceFromCache()

        assertTrue("expected Deferred, got $result", result is SyncResult.Deferred)
        assertTrue("the appliers ran with no policy at all", applier.applied.isEmpty())
    }

    // ---- the daily quota, offline ------------------------------------------------------------

    /**
     * FR-3 and FR-9 together. The server's `used_minutes_today` is only ever as fresh as the last
     * report this phone managed to deliver, so a child with no signal would carry the morning's
     * number all day and the limit would never be reached — a device that looks perfectly managed
     * and enforces no quota at all.
     */
    @Test
    fun `the quota is reached from the device's own measurement when the server's is stale`() {
        answerWithQuota(limit = 60, serverUsed = 5)

        val result = synchronizer(localUsedMinutes = { 90 }).sync() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_QUOTA, result.state.suspendReason)
        assertEquals(90, result.state.usedMinutes)
        assertTrue("the quota suspended nothing", result.state.suspendedPackages.isNotEmpty())
    }

    /**
     * The double-count guard, and the reason the two numbers are combined with `max` rather than
     * added. Forty minutes the device measured and then reported back are the *same* forty minutes
     * the server is holding; summing them would exhaust an hour's quota the child had not spent.
     */
    @Test
    fun `the device's minutes are not added to the ones it already reported`() {
        answerWithQuota(limit = 60, serverUsed = 40)

        val result = synchronizer(localUsedMinutes = { 40 }).sync() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_NONE, result.state.suspendReason)
        assertEquals(40, result.state.usedMinutes)
    }

    /** A device whose usage storage was cleared falls back to the server's number, not to zero. */
    @Test
    fun `the server's number wins when the device has measured less`() {
        answerWithQuota(limit = 60, serverUsed = 70)

        val result = synchronizer(localUsedMinutes = { 0 }).sync() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_QUOTA, result.state.suspendReason)
        assertEquals(70, result.state.usedMinutes)
    }

    /**
     * A device that cannot measure at all returns 0 — never a guess — and 0 changes nothing. Usage
     * access is a per-device grant that a parent may not have given yet, and the failure of that
     * grant must not be a phone that enforces a quota nobody spent.
     */
    @Test
    fun `a device that cannot measure changes nothing`() {
        answerWithQuota(limit = 60, serverUsed = 30)

        val result = synchronizer(localUsedMinutes = { 0 }).sync() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_NONE, result.state.suspendReason)
        assertEquals(30, result.state.usedMinutes)
    }

    /** The offline path is the point: the cached policy is where a phone with no signal lives. */
    @Test
    fun `the device's own minutes reach the quota with no server at all`() {
        cache.stored = inputAt(SERVER_NOW, version = 4, dailyLimitMinutes = 60, usedMinutesToday = 5)
        server.stopAnswering()

        val result = synchronizer(localUsedMinutes = { 90 }).enforceFromCache() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_QUOTA, result.state.suspendReason)
    }

    /**
     * The policy is handed to the measurement, not just its result. The device keys its usage by the
     * *policy's* timezone — a day boundary taken from the phone's own zone would file a child's
     * evening under a key the server's quota never reads.
     */
    @Test
    fun `the policy is passed to the measurement so it can find the right day`() {
        answerWithQuota(limit = 60, serverUsed = 0)
        val seen = mutableListOf<Input>()

        synchronizer(localUsedMinutes = { seen += it; 0 }).sync()

        assertEquals(1, seen.size)
        assertEquals("Europe/Zurich", seen.single().settings.timezone)
    }

    /** A quota of zero is "no limit", and no measurement can turn it into one. */
    @Test
    fun `a device with no daily limit is never suspended by its own measurement`() {
        answerWithQuota(limit = 0, serverUsed = 0)

        val result = synchronizer(localUsedMinutes = { 600 }).sync() as SyncResult.Applied

        assertEquals(EnforcementEngine.REASON_NONE, result.state.suspendReason)
        assertTrue(result.state.suspendedPackages.isEmpty())
    }

    // ---- what the heartbeat claims -----------------------------------------------------------

    /**
     * The version reported is the one this device has *in effect*, not the one it just computed.
     *
     * They differ exactly when an apply failed, and that difference is the only signal the console
     * has that a phone it lists as up to date is not. Reporting the computed version would erase it.
     */
    @Test
    fun `a failed apply does not advance the version the heartbeat claims`() {
        cache.applied = 4
        applier.outcome = ApplyOutcome("tried", mapOf("no_debugging" to "rejected by the OEM"))

        val result = synchronizer().sync() as SyncResult.Applied

        assertFalse("a failed apply reported ok", result.outcome.ok)
        assertEquals("the device claimed a version it did not apply", 4L, cache.applied)
        assertEquals("4", heartbeatField("policy_version"))
    }

    @Test
    fun `a clean apply claims the new version`() {
        cache.applied = 4

        synchronizer().sync()

        assertEquals(7L, cache.applied)
        // 7, not 4: the write happens before the heartbeat is sent, so what the server is told is
        // what the device now has, and a phone is never a sync behind its own report.
        assertEquals("7", heartbeatField("policy_version"))
    }

    /** Telemetry that could not be read is omitted, never sent as a zero. */
    @Test
    fun `unmeasured telemetry is absent from the heartbeat rather than fabricated`() {
        val silent = Synchronizer(
            api,
            cache,
            applier,
            recovery,
            telemetry = { DeviceTelemetry() },
            now = { DEVICE_NOW },
        )

        silent.sync()

        val body = server.requests.last { it.path.endsWith("/heartbeat") }.body
        assertFalse("an unread battery level was sent as a number: $body", body.contains("battery_level"))
        assertFalse("an unread screen state was sent as a boolean: $body", body.contains("screen_on"))
    }

    /** A heartbeat that fails does not undo an apply that worked. The phone is enforcing either way. */
    @Test
    fun `a heartbeat that cannot be sent does not undo the apply`() {
        server.answerWith { request ->
            if (request.path.endsWith("/heartbeat")) {
                HttpResponse(503, reason = "Service Unavailable", body = """{"error":"down"}""")
            } else {
                HttpResponse(200, body = policyBody())
            }
        }

        val result = synchronizer().sync() as SyncResult.Applied

        assertEquals(1, applier.applied.size)
        assertEquals(7L, cache.applied)
        assertEquals("an unreachable server was reported as having no work", 0, result.pendingCommands)
    }

    /** A credential revoked between the two calls is still a revoked credential. */
    @Test
    fun `a heartbeat the server refuses reports the refusal`() {
        server.answerWith { request ->
            if (request.path.endsWith("/heartbeat")) {
                HttpResponse(401, reason = "Unauthorized", body = """{"error":"unauthorized"}""")
            } else {
                HttpResponse(200, body = policyBody())
            }
        }

        val result = synchronizer().sync()

        assertTrue("expected Refused, got $result", result is SyncResult.Refused)
        // The apply still happened and is still in effect. A refusal at the heartbeat says the
        // device may no longer be managed; it does not say the policy it already applied is wrong.
        assertEquals(1, applier.applied.size)
    }

    // ---- a recovered device (FR-12.2) --------------------------------------------------------

    /**
     * The whole point of a recovery: the phone stops enforcing, and stays that way with no network.
     *
     * `DesiredState()` is the released state, and asserting on it rather than on the result type is
     * deliberate — a `Released` that applied the cached policy anyway would be the same brick with a
     * new label on it.
     */
    @Test
    fun `a recovered device applies the released state instead of its cached policy`() {
        cache.stored = inputAt("2026-08-17T22:00:00+02:00", version = 4)
        cache.applied = 4
        recoveryStore.setActiveSince(RECOVERED_AT)
        server.stopAnswering()

        val result = synchronizer().sync() as SyncResult.Released

        assertEquals(DesiredState(), applier.applied.single())
        assertEquals(RECOVERED_AT, result.since)
        assertTrue("the release reported problems: ${result.outcome}", result.outcome.ok)
    }

    /**
     * `policy_version` on the released state is 0, and writing that would tell the console this
     * phone is running policy v0 — a version that exists and is not this one.
     *
     * The last version the device actually enforced stays on record, which is the true answer to
     * "what was in effect before somebody recovered it".
     */
    @Test
    fun `a release does not claim a policy version`() {
        cache.stored = inputAt(SERVER_NOW, version = 4)
        cache.applied = 4
        recoveryStore.setActiveSince(RECOVERED_AT)
        server.stopAnswering()

        synchronizer().sync()

        assertEquals(4L, cache.applied)
    }

    /** The bedtime alarm on a released phone keeps releasing it, rather than re-enforcing at 21:00. */
    @Test
    fun `the alarm path releases a recovered device too`() {
        cache.stored = inputAt("2026-08-17T22:00:00+02:00", version = 4)
        recoveryStore.setActiveSince(RECOVERED_AT)

        val result = synchronizer().enforceFromCache()

        assertTrue("expected Released, got $result", result is SyncResult.Released)
        assertEquals(DesiredState(), applier.applied.single())
    }

    /**
     * Reaching the control plane is what ends a recovery (FR-12.2) — not the network coming back,
     * and not a request succeeding, but a policy that came from the server and is about to be
     * enforced.
     *
     * So the device is managed again from this sync onwards, and the parent sees it on the console
     * in the same moment.
     */
    @Test
    fun `a policy fetched from the server ends the recovery and re-enforces`() {
        recoveryStore.setActiveSince(RECOVERED_AT)

        val result = synchronizer(now = "2026-08-17T22:00:00+02:00").sync() as SyncResult.Applied

        assertEquals(PolicySource.SERVER, result.source)
        assertNull("the phone reached the server and stayed unmanaged", recoveryStore.activeSince())
        assertEquals(
            "a re-managed device did not re-enforce bedtime",
            EnforcementEngine.REASON_BEDTIME,
            applier.applied.single().suspendReason,
        )
    }

    /**
     * A phone that has a connection but is refused by the server stays released.
     *
     * This is the case the obvious implementation gets wrong. "Clear the flag when the device next
     * reaches the network" is one line shorter and re-enforces a phone the server has just declined
     * to talk to — which is precisely the situation recovery exists for.
     */
    @Test
    fun `a refused device stays released`() {
        cache.stored = inputAt(SERVER_NOW, version = 4)
        recoveryStore.setActiveSince(RECOVERED_AT)
        server.answerWith { HttpResponse(401, reason = "Unauthorized", body = """{"error":"unauthorized"}""") }

        val result = synchronizer().sync()

        assertTrue("expected Refused, got $result", result is SyncResult.Refused)
        assertEquals(RECOVERED_AT, recoveryStore.activeSince())
        assertTrue("a refused device was re-enforced", applier.applied.isEmpty())
    }

    /**
     * A server that cannot be reached does not end a recovery either, and the cached policy is not
     * applied on the way past.
     *
     * The ordering inside `applyFrom` is what this pins: the flag is checked after the source is
     * known, so a cache fallback reaches the release rather than the engine.
     */
    @Test
    fun `an unreachable server leaves a recovered device released`() {
        cache.stored = inputAt("2026-08-17T22:00:00+02:00", version = 4)
        recoveryStore.setActiveSince(RECOVERED_AT)
        server.stopAnswering()

        repeat(3) { synchronizer().sync() }

        assertEquals(RECOVERED_AT, recoveryStore.activeSince())
        assertEquals(
            "an unreachable server re-enforced the cached policy",
            listOf(DesiredState(), DesiredState(), DesiredState()),
            applier.applied,
        )
    }

    // ---- when the server was last reached (FR-13.4) -------------------------------------------

    /**
     * The stamp is a literal, not `OffsetDateTime.parse(DEVICE_NOW)`.
     *
     * Deriving the expected value the same way the production code derives the stored one is a test
     * that agrees with whatever the conversion does, including converting wrongly. `1786989600000`
     * is 2026-08-17T20:00:00+02:00 as epoch millis, computed elsewhere, and a change to the time
     * zone handling has to change this number by hand.
     */
    @Test
    fun `a policy that came from the server stamps when the server was reached`() {
        synchronizer().sync()

        assertEquals(1_786_989_600_000L, cache.contact)
    }

    /**
     * The alarm path runs every fifteen minutes on a phone with no signal. If it stamped, the status
     * screen would report a device that has not spoken to the server in a week as having reached it
     * a quarter of an hour ago — which is the exact question the line exists to answer, answered
     * backwards.
     */
    @Test
    fun `enforcing the cached policy is not contact, however often it runs`() {
        cache.stored = inputAt(SERVER_NOW)
        cache.contact = A_WEEK_AGO

        synchronizer().enforceFromCache()
        synchronizer().enforceFromCache()

        assertEquals(A_WEEK_AGO, cache.contact)
    }

    /** Same reasoning for the fallback inside `sync()`: the fetch failed, so nothing was reached. */
    @Test
    fun `a sync that falls back to the cache does not stamp contact`() {
        cache.stored = inputAt(SERVER_NOW)
        cache.contact = A_WEEK_AGO
        server.stopAnswering()

        val result = synchronizer().sync()

        assertTrue("expected the cached policy, got $result", result is SyncResult.Applied)
        assertEquals(A_WEEK_AGO, cache.contact)
    }

    /**
     * A revoked credential reaches the server and comes back with nothing.
     *
     * TCP says the phone reached *a* host; this line claims it reached the family settings, and a
     * device the server no longer recognises has not. Stamping here would show a phone whose
     * enrollment has been deleted as freshly synced — green, current, and enforcing a policy nobody
     * can change any more.
     */
    @Test
    fun `a refused credential is not contact`() {
        server.answerWith { HttpResponse(401, reason = "Unauthorized", body = """{"error":"unknown device"}""") }
        cache.contact = A_WEEK_AGO

        val result = synchronizer().sync()

        assertTrue("expected a refusal, got $result", result is SyncResult.Refused)
        assertEquals(A_WEEK_AGO, cache.contact)
    }

    /**
     * A clock this device cannot parse stamps 0, which the status screen renders as *never*.
     *
     * The safe direction, and the reason it is asserted rather than left to `getOrDefault`: the
     * alternative fallbacks all read as a healthy phone. `now()` from a second clock, the server's
     * instant, `System.currentTimeMillis()` — each of them would show a device with a broken clock
     * as freshly synced, and a broken clock is one of the few things that can stop enforcement
     * outright.
     */
    @Test
    fun `a clock this device cannot read stamps never, not now`() {
        synchronizer(now = "yesterday evening").sync()

        assertEquals(0L, cache.contact)
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private val events = mutableListOf<String>()

    private fun answerWithQuota(limit: Int, serverUsed: Int) {
        server.answerWith { request ->
            if (request.path.endsWith("/heartbeat")) {
                HttpResponse(200, body = """{"policy_version":99,"pending_commands":2}""")
            } else {
                HttpResponse(
                    200,
                    body = policyBody(
                        inputAt(SERVER_NOW, dailyLimitMinutes = limit, usedMinutesToday = serverUsed)
                    ),
                )
            }
        }
    }

    private fun heartbeatField(name: String): String? =
        Regex(""""$name"\s*:\s*(-?\d+)""")
            .find(server.requests.last { it.path.endsWith("/heartbeat") }.body)
            ?.groupValues
            ?.get(1)

    private fun policyBody(input: Input = inputAt(SERVER_NOW)): String {
        val json = kotlinx.serialization.json.Json { encodeDefaults = true }
        // The server's own `desired` is deliberately sent as something this device must NOT apply:
        // an empty state. Anything that read `desired` instead of recomputing from `input` would
        // hand the appliers an unlocked phone, and every assertion about `suspendReason` here would
        // change. That is the point — the field is a tripwire, not a fixture detail.
        return """{"desired":${json.encodeToString(DesiredState.serializer(), DesiredState())},""" +
            """"input":${json.encodeToString(Input.serializer(), input)}}"""
    }

    private fun inputAt(
        now: String,
        version: Long = 7,
        dailyLimitMinutes: Int = 0,
        usedMinutesToday: Int = 0,
    ): Input = Input(
        settings = Settings(
            bedtimeEnabled = true,
            bedtimeStart = "21:00",
            bedtimeEnd = "07:00",
            timezone = "Europe/Zurich",
            dailyLimitMinutes = dailyLimitMinutes,
            version = version,
        ),
        installed = listOf(App(pkg = "com.example.game")),
        usedMinutesToday = usedMinutesToday,
        now = now,
    )

    private companion object {
        /** 20:00 Zurich: before bedtime. */
        const val SERVER_NOW = "2026-08-17T20:00:00+02:00"

        /** The same instant, so a test that does not care about the clock does not depend on it. */
        const val DEVICE_NOW = SERVER_NOW

        /** When a recovery was entered, epoch millis. A fixed number the assertions can name. */
        const val RECOVERED_AT = 1_755_000_000_000L

        /** A contact stamp old enough that no test could confuse it with one written just now. */
        const val A_WEEK_AGO = 1_786_384_800_000L

        val TELEMETRY = DeviceTelemetry(
            batteryLevel = 62,
            charging = false,
            screenOn = true,
            connectivity = "wifi",
        )
    }
}

/** [PolicyCache] in memory, with a hook so ordering against the applier can be observed. */
private class FakeCache : PolicyCache {
    var stored: Input? = null
    var applied: Long = 0
    var contact: Long = 0
    var onSave: () -> Unit = {}

    override fun load(): Input? = stored

    override fun save(input: Input) {
        onSave()
        stored = input
    }

    override fun appliedVersion(): Long = applied

    override fun recordApplied(version: Long) {
        applied = version
    }

    override fun lastServerContact(): Long = contact

    override fun recordServerContact(atEpochMillis: Long) {
        contact = atEpochMillis
    }

    override fun clear() {
        stored = null
        applied = 0
        contact = 0
    }
}

/**
 * Records every state it is handed.
 *
 * The list, not a boolean: several tests assert that the appliers were *not* called, and "not
 * called" and "called with an empty state" are the two outcomes that must never be confused.
 */
private class RecordingApplier : StateApplier {
    val applied = mutableListOf<DesiredState>()
    var outcome = ApplyOutcome("ok")
    var onApply: () -> Unit = {}

    override fun apply(state: DesiredState): ApplyOutcome {
        onApply()
        applied += state
        return outcome
    }
}
