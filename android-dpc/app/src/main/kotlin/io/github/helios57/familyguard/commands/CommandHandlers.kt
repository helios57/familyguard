package io.github.helios57.familyguard.commands

import io.github.helios57.familyguard.net.LocationRequest
import io.github.helios57.familyguard.policy.LockManager
import io.github.helios57.familyguard.update.UpdateOutcome
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

/**
 * Every command type this device implements, wired to the pieces that carry it out (FR-9).
 *
 * Four of the eight types do nothing on the device beyond re-syncing, and that is the design rather
 * than a shortcut. `BLOCK_YOUTUBE_ALL`, `UNBLOCK_YOUTUBE_ALL` and `UNLOCK_DEVICE` are *state*
 * changes: the server has already flipped the setting by the time the command is queued, so the
 * device's part is to fetch the new policy and apply it — the same path a scheduled sync takes. A
 * handler that reached for the platform directly instead would be a second implementation of the
 * same rule, and the two would diverge the first time one of them was changed. `SYNC_POLICY` is
 * that path named out loud.
 *
 * The two that *are* device actions — the siren and the fix — hold no state on the server at all,
 * which is why they must be commands and cannot be policy: "ring now" is not a fact about a phone,
 * it is an event.
 */
class CommandHandlers(
    private val lock: LockManager,
    private val siren: SirenController,
    private val location: LocationProbe,
    private val reportLocation: (LocationRequest) -> Unit,
    /**
     * Re-fetches and re-applies the policy. Returns null when that worked, or the reason it did not.
     *
     * **This must not be called with the caller's sync lock held.** Four of the eight handlers
     * re-sync, so a lock taken around the drain and re-entered here is a deadlock on the commonest
     * command in the product.
     */
    private val resync: () -> String?,
    /**
     * Downloads and stages the DPC the server hosts (FR-15.2). Null on a device with no updater
     * wired, which answers `UPDATE_APP` as unimplemented rather than as silently done.
     */
    private val update: (() -> UpdateOutcome)? = null,
    private val now: () -> Long = { System.currentTimeMillis() },
) {

    fun asMap(): Map<String, CommandHandler> = mapOf(
        // The server has already set the standing parent-lock flag; this is what makes it happen
        // now rather than at the next sync. `LockApplier` is what keeps it true afterwards.
        LOCK_NOW to CommandHandler {
            val outcome = lock.lock()
            outcome.failure?.let { CommandOutcome.Failed(it) }
                ?: CommandOutcome.Done(result("state" to outcome.summary, "note" to outcome.note))
        },

        // No platform call exists to dismiss a keyguard — see LockManager. The flag is already
        // cleared on the server, so re-syncing is the whole of the device's part, and the phone
        // stops re-locking from this moment.
        UNLOCK_DEVICE to resyncing("the parent lock is cleared; this device will stop re-locking"),

        TRIGGER_ALARM to CommandHandler {
            val outcome = siren.start()
            outcome.failure?.let { CommandOutcome.Failed(it) }
                ?: CommandOutcome.Done(result("state" to outcome.summary, "note" to outcome.note))
        },

        STOP_ALARM to CommandHandler {
            val outcome = siren.stop()
            outcome.failure?.let { CommandOutcome.Failed(it) }
                ?: CommandOutcome.Done(result("state" to outcome.summary))
        },

        LOCATE_NOW to CommandHandler { locate() },

        BLOCK_YOUTUBE_ALL to resyncing("the YouTube killswitch is on"),
        UNBLOCK_YOUTUBE_ALL to resyncing("the YouTube killswitch is off"),
        SYNC_POLICY to resyncing("policy re-fetched and applied"),

        // The one command whose result describes the future tense, and honestly so. Everything that
        // can be checked has been checked by the time this returns — the download, its checksum, its
        // signature, its version — and what is left is a platform call that ends this process. So
        // the acknowledgement says what is about to be installed, and the phone's next heartbeat,
        // carrying app_version_code, is what says it happened. See CommandOutcome.Done.after.
        UPDATE_APP to CommandHandler { updateApp() },
    )

    private fun updateApp(): CommandOutcome {
        val run = update ?: return CommandOutcome.Failed("this device has no updater wired")
        return when (val outcome = run()) {
            is UpdateOutcome.Refused -> CommandOutcome.Failed(outcome.reason)
            is UpdateOutcome.AlreadyCurrent -> CommandOutcome.Done(
                result(
                    "state" to "already running the build this server hosts",
                    "version" to outcome.identity.versionName,
                    "build" to outcome.identity.versionCode.toString(),
                )
            )
            is UpdateOutcome.Staged -> CommandOutcome.Done(
                result(
                    "state" to "downloaded and verified; installing now",
                    "version" to outcome.identity.versionName,
                    "build" to "${outcome.fromVersionCode} \u2192 ${outcome.identity.versionCode}",
                ),
                after = outcome.commit,
            )
        }
    }

    private fun resyncing(summary: String) = CommandHandler {
        val problem = resync()
        if (problem == null) CommandOutcome.Done(result("state" to summary))
        else CommandOutcome.Failed(problem)
    }

    private fun locate(): CommandOutcome {
        val located = when (val result = location.probe()) {
            is ProbeResult.Unavailable -> return CommandOutcome.Failed(result.reason)
            is ProbeResult.Located -> result
        }
        val fix = located.fix
        val capturedAt = rfc3339(fix.capturedAtEpochMillis)
        // Reported before it is acknowledged, and a failure to report is a failed command. The ack
        // carries no coordinates: the position belongs in the row the console draws a map from, and
        // duplicating it into a free-text result is two sources for one fact.
        try {
            reportLocation(
                LocationRequest(
                    latitude = fix.latitude,
                    longitude = fix.longitude,
                    accuracyM = fix.accuracyM,
                    capturedAt = capturedAt,
                )
            )
        } catch (e: Exception) {
            return CommandOutcome.Failed(
                "a position was obtained and could not be delivered (${e.message ?: e.javaClass.simpleName})"
            )
        }
        return CommandOutcome.Done(
            result(
                "captured_at" to capturedAt,
                // Stated even when it is zero. "fresh" and "the age was not computed" are different,
                // and a parent reading a map is deciding whether to walk somewhere.
                "age_seconds" to (located.ageMillis / 1000).toString(),
                "source" to if (located.fresh) "gnss" else "last known position",
            )
        )
    }

    /** Drops the pairs whose value is null or blank, so an absent note is absent rather than "". */
    private fun result(vararg pairs: Pair<String, String?>): Map<String, String> =
        pairs.mapNotNull { (key, value) -> value?.takeIf { it.isNotBlank() }?.let { key to it } }.toMap()

    private fun rfc3339(epochMillis: Long): String =
        RFC3339.format(Instant.ofEpochMilli(epochMillis).atOffset(ZoneOffset.UTC))

    companion object {
        const val LOCK_NOW = "LOCK_NOW"
        const val UNLOCK_DEVICE = "UNLOCK_DEVICE"
        const val TRIGGER_ALARM = "TRIGGER_ALARM"
        const val STOP_ALARM = "STOP_ALARM"
        const val LOCATE_NOW = "LOCATE_NOW"
        const val BLOCK_YOUTUBE_ALL = "BLOCK_YOUTUBE_ALL"
        const val UNBLOCK_YOUTUBE_ALL = "UNBLOCK_YOUTUBE_ALL"
        const val SYNC_POLICY = "SYNC_POLICY"
        const val UPDATE_APP = "UPDATE_APP"

        /**
         * UTC, seconds precision, spelled out — the same reasoning as `Synchronizer.RFC3339`.
         * `OffsetDateTime.toString()` omits the seconds field when it is zero, which is valid
         * RFC 3339 and a value that differs from every other instant this app sends once a minute.
         */
        private val RFC3339: DateTimeFormatter =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")
    }
}
