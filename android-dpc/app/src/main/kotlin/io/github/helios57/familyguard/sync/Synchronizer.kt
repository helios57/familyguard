package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.enforce.InvalidPolicyInput
import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.ApiException
import io.github.helios57.familyguard.net.HeartbeatRequest
import io.github.helios57.familyguard.recovery.RecoveryMode
import io.github.helios57.familyguard.recovery.releasedState
import java.io.IOException
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter

/** Whether the policy that was enforced came from the server just now, or from the last sync. */
enum class PolicySource { SERVER, CACHE }

/**
 * What one sync did.
 *
 * Five outcomes, and exactly two of them mean the device is enforcing nothing — [Deferred], which
 * is a phone that has never synced, and [Released], which is a phone a parent recovered. Both say
 * so rather than looking like a quiet success, which is the only reason they are separate variants
 * and not an `Applied` with an empty state.
 */
sealed interface SyncResult {
    /**
     * A desired state was computed and handed to the appliers. [outcome] says whether the device
     * agreed — a state that was applied with problems is still an enforced state, and saying so is
     * the difference between a phone that is 90% managed and one that is not managed at all.
     */
    data class Applied(
        val state: DesiredState,
        val source: PolicySource,
        val outcome: ApplyOutcome,
        val pendingCommands: Int = 0,
    ) : SyncResult

    /** The server refused this device's credential. Waiting cannot fix it; the caller must stop. */
    data class Refused(val cause: ApiException) : SyncResult

    /** Nothing could be fetched and nothing was cached — a device that has never synced, offline. */
    data class Deferred(val cause: Exception) : SyncResult

    /**
     * The policy could not be evaluated: an unknown timezone, a bedtime that is not HH:MM. The
     * previous state is left in effect rather than replaced with an empty one.
     */
    data class Rejected(val cause: InvalidPolicyInput) : SyncResult

    /**
     * A parent entered the recovery code and the server has not been reached since (FR-12.2). The
     * released state was applied; the cached policy was deliberately not.
     */
    data class Released(val outcome: ApplyOutcome, val since: Long?) : SyncResult
}

/**
 * One sync: fetch the policy, cache it, compute the desired state locally, apply it, report back.
 *
 * **The device computes its own desired state even when the server just sent one.** The response
 * carries both, and this class uses the input. That is not distrust of the server — it is the same
 * decision as FR-9: the phone must produce the same answer offline as online, and the only way to
 * be sure it does is for the online path to run through the offline code. A device that applied the
 * server's `desired` when connected and computed its own when not would have two behaviours, and
 * the one that runs at 21:00 in a tunnel is the one nobody ever tested.
 *
 * Every one of the four failure modes leaves the phone enforcing something. There is deliberately no
 * path here that reaches the appliers with an empty [DesiredState]: an empty state clears every
 * managed restriction, so "the server did not answer" would unlock the phone.
 */
class Synchronizer(
    private val api: ApiClient,
    private val cache: PolicyCache,
    private val applier: StateApplier,
    /**
     * Whether a parent has recovered this device (FR-12).
     *
     * Required rather than defaulted to "never active", because the default that makes the tests
     * compile is also the one that silently turns the escape hatch off: a device wired without it
     * would re-enforce bedtime on the next alarm, minutes after somebody typed the code, and
     * nothing anywhere would be red.
     */
    private val recovery: RecoveryMode,
    private val telemetry: () -> DeviceTelemetry = { DeviceTelemetry() },
    /**
     * This device's clock, as RFC 3339. Injected so the tests can move it, and read *per sync*
     * rather than captured — see [applyFrom], where using the server's instant instead is the bug
     * this exists to make impossible.
     */
    private val now: () -> String = { OffsetDateTime.now().format(RFC3339) },
    /**
     * What this device has measured for the policy's current day, in minutes.
     *
     * The server's `used_minutes_today` is only ever as fresh as the last report this phone managed
     * to deliver, so a phone with no signal would carry yesterday evening's number all day and the
     * quota would simply never be reached (FR-3, FR-9). The device knows better: it measured the
     * minutes itself.
     *
     * The two are combined with `max`, which is the same rule the server's own upsert uses
     * (`GREATEST(stored, reported)`). Neither side can roll the other backwards: a device whose
     * storage was cleared falls back to the server's number, and a device that cannot measure at all
     * returns 0 and changes nothing. Summing them instead would double-count every minute that has
     * already been reported, and would exhaust a quota the child had not spent.
     *
     * Returns 0 when nothing is measured — never a guess. See `UsageTracker`.
     */
    private val localUsedMinutes: (Input) -> Int = { 0 },
) {

    /**
     * Fetches, caches, computes, applies, heartbeats.
     *
     * A fetch that fails for a reason waiting could fix falls through to the cached policy, so a
     * phone with no signal keeps enforcing the last thing the parent set (FR-9). A fetch that fails
     * because the credential is gone does not: there is nothing to wait for.
     */
    fun sync(): SyncResult {
        val fetched = try {
            val response = api.policy()
            // Cached *before* it is applied. A process killed halfway through applying comes back
            // to the new policy rather than to the one before it, and the applier is idempotent, so
            // re-applying costs a few platform calls and never a wrong state.
            cache.save(response.input)
            // Stamped here — on receipt — and nowhere else. Not in `applyFrom`, which also runs for
            // a cached policy and would then report a phone that has not seen the server in a week
            // as having reached it a minute ago; and not after the apply, because a policy that
            // arrived and failed to apply is still proof the network and the credential work. This
            // is what the status screen shows as "last reached the family settings" (FR-13.4).
            cache.recordServerContact(epochMillisOf(now()))
            response.input
        } catch (e: ApiException) {
            if (!e.retryable) return SyncResult.Refused(e)
            null
        } catch (e: IOException) {
            null
        }

        val input = fetched ?: cache.load()
            ?: return SyncResult.Deferred(
                IOException("the policy could not be fetched and this device has never cached one")
            )
        return applyFrom(input, if (fetched != null) PolicySource.SERVER else PolicySource.CACHE)
    }

    /**
     * Re-applies the cached policy against the current clock, with no network at all.
     *
     * This is the path the bedtime alarm takes (FR-9, NFR-10): nothing about the policy has changed,
     * but 21:00 has arrived, so the same input produces a different desired state. It is a separate
     * method rather than `sync()` with a flag because a phone waking at 21:00 in a tunnel must not
     * spend its radio finding that out.
     */
    fun enforceFromCache(): SyncResult {
        val input = cache.load()
            ?: return SyncResult.Deferred(IOException("this device has never cached a policy"))
        return applyFrom(input, PolicySource.CACHE)
    }

    /**
     * The device clock as epoch millis, taken from the same string the engine is given.
     *
     * Derived rather than injected as a second clock, because two clocks that must agree are two
     * clocks that can disagree — and a "last reached the family settings" that contradicts the
     * policy the same sync applied is worse than not having the line at all.
     *
     * A string this class cannot parse stamps 0, which the status screen renders as *never*. That
     * is the safe direction: an unparseable clock is a bug, and a bug should show up as a phone
     * that appears out of contact, never as one that appears freshly synced.
     */
    private fun epochMillisOf(iso: String): Long =
        runCatching { OffsetDateTime.parse(iso).toInstant().toEpochMilli() }.getOrDefault(0L)

    private fun applyFrom(input: Input, source: PolicySource): SyncResult {
        // Reaching the control plane is what ends a recovery (FR-12.2), and this is the only place
        // that fact is available: not "the network came back", not "a request succeeded", but a
        // policy that came from the server and is about to be enforced. Cleared before the state is
        // applied, so a crash between the two leaves a managed device rather than a released one.
        if (source == PolicySource.SERVER) recovery.clear()
        if (recovery.active()) return release()

        // The device's clock, always — never `input.now`. The server's instant is the moment the
        // response was built, which for a cached policy is hours or days ago, and for a fresh one is
        // still not now. Computing bedtime from it would mean a phone that fetched at 20:00 and woke
        // at 21:00 decided it was 20:00.
        val state = try {
            EnforcementEngine.compute(
                input.copy(
                    now = now(),
                    // See `localUsedMinutes`: max, never sum, and never a replacement.
                    usedMinutesToday = maxOf(input.usedMinutesToday, localUsedMinutes(input)),
                )
            )
        } catch (e: InvalidPolicyInput) {
            // Refusing beats guessing. The alternative — a default state — is an unlocked phone
            // produced by a typo in the console, with nothing anywhere reporting a problem.
            return SyncResult.Rejected(e)
        }

        val outcome = applier.apply(state)
        // Only a clean apply advances the version this device claims. Recording it regardless would
        // make the console show a phone as running v7 while three of v7's restrictions never took —
        // and the parent's only evidence would be the rule not happening.
        if (outcome.ok) cache.recordApplied(state.policyVersion)

        val pending = try {
            heartbeat()
        } catch (e: ApiException) {
            if (!e.retryable) return SyncResult.Refused(e)
            0
        } catch (_: IOException) {
            0
        }
        return SyncResult.Applied(state, source, outcome, pending)
    }

    /**
     * Applies the released state (FR-12.2), and only ever reaches here from the cache path.
     *
     * Three things it deliberately does not do:
     *
     * **It does not record an applied version.** `policy_version` is 0 on the released state, and
     * writing that would tell the console this phone is running policy v0 — a version that exists
     * and is not this one. The last version the device actually enforced stays on record, which is
     * the true answer to "what was in effect before somebody recovered it".
     *
     * **It does not heartbeat.** This path is reached only when the policy fetch failed, so the
     * link has just refused a request; the same reasoning `ConnectionService` applies to usage
     * reports applies here. The console learns about the release from the recovery event, which is
     * queued and retried until it lands.
     *
     * **It does not re-apply on every tick beyond what the appliers already do.** They are
     * idempotent, so a phone released for a week runs the same clear every fifteen minutes and
     * changes nothing — which is what keeps a policy re-applied by something else from sticking.
     */
    private fun release(): SyncResult {
        val outcome = applier.apply(releasedState())
        return SyncResult.Released(outcome, recovery.activeSince())
    }

    /**
     * @return the number of commands the server says are waiting.
     *
     * The reported `policy_version` is [PolicyCache.appliedVersion] — what this device has in
     * effect — and not the version of the state it just computed. They are the same number on a
     * healthy device and differ exactly when it is worth knowing.
     */
    private fun heartbeat(): Int {
        val t = telemetry()
        // The `policy_version` the response carries is not read back into the cache: it is a record
        // of what this device reported a moment ago, so believing it would close the loop on our own
        // claim. The value only the server knows is the command count.
        return api.heartbeat(
            HeartbeatRequest(
                batteryLevel = t.batteryLevel,
                charging = t.charging,
                screenOn = t.screenOn,
                connectivity = t.connectivity,
                policyVersion = cache.appliedVersion(),
                appVersionName = t.appVersionName,
                appVersionCode = t.appVersionCode,
            )
        ).pendingCommands
    }

    private companion object {
        /**
         * The format the engine parses and the Go side emits. Spelled out rather than left to
         * `OffsetDateTime.toString()`, which drops the seconds field whenever it is zero — legal
         * RFC 3339, and a value that differs from every other instant this app produces once a
         * minute.
         */
        val RFC3339: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")
    }
}

/**
 * What the device could measure about itself, for the heartbeat.
 *
 * Every field is nullable and the client omits nulls, because "the screen state was not read" and
 * "the screen is off" are different facts and the console draws them differently. A default of
 * `false` for an unmeasured field is a fabricated measurement.
 */
data class DeviceTelemetry(
    val batteryLevel: Int? = null,
    val charging: Boolean? = null,
    val screenOn: Boolean? = null,
    val connectivity: String = "",
    /**
     * The DPC build this process is (FR-15.4). Read from the package manager rather than from
     * `BuildConfig`, for one reason: after a self-update the running code and the installed package
     * are the same thing again only once the process has restarted, and the package manager is the
     * authority on which of the two the phone actually has.
     */
    val appVersionName: String = "",
    val appVersionCode: Long = 0,
)
