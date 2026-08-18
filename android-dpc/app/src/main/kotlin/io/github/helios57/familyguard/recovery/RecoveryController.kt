package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.RecoveryMaterial
import io.github.helios57.familyguard.sync.ApplyOutcome

/** What happened to one submitted code. Everything the screen shows comes from one of these. */
sealed interface RecoveryResult {

    /** The code was right. Enforcement is released until the device next reaches the server. */
    data class Released(val outcome: ApplyOutcome) : RecoveryResult

    /** The code was wrong. [wait] is what the next attempt must wait, 0 if it may be made now. */
    data class Rejected(val consecutiveFailures: Int, val wait: LockoutStatus) : RecoveryResult

    /** Too many wrong codes. Nothing was checked, so nothing was recorded. */
    data class TooManyAttempts(val remainingMillis: Long) : RecoveryResult

    /**
     * No code could ever be accepted on this device, and the reason is not the code. A device that
     * never enrolled, or one enrolled against a server that issued no recovery material.
     */
    data class Unavailable(val reason: String) : RecoveryResult
}

/**
 * The whole of FR-12 on the device side, in one place that a JVM test can drive.
 *
 * Everything Android — the screen, the encrypted files, the device-policy calls — is behind the
 * four things this takes: where the material comes from, what rate-limits attempts, what remembers
 * the release, and what actually applies it. The activity is then a text field and a button.
 *
 * **Nothing here touches the network** (FR-12.1). The material was stored at enrollment, the
 * verification is local, and the release is applied by the device-policy calls on this phone. That
 * is the entire requirement: the case a parent needs this in is a phone that is locked down *and*
 * cannot reach the control plane, so any step that asked the server a question would be a step that
 * fails exactly when it is needed. The one thing that does reach the server — reporting the attempt
 * (FR-12.5) — is queued and happens afterwards, and its failure cannot block the release.
 *
 * The order of operations after a code verifies is the part worth reading twice:
 *
 * 1. the attempt is journalled,
 * 2. recovery mode is set,
 * 3. the released state is applied.
 *
 * It is written that way so that every prefix of it is a safe place to be killed. A process that
 * dies after (1) has the attempt to report and is still enforcing — correct, because nothing was
 * released. After (2) it is not yet released but will be by the next sync or alarm, because
 * `Synchronizer` reads the flag on every path. Only the reverse order has a bad prefix: releasing
 * first and dying would leave a phone unmanaged with no record of why, and no flag to end it.
 */
class RecoveryController(
    private val material: () -> RecoveryMaterial?,
    private val lockout: RecoveryLockout,
    private val mode: RecoveryMode,
    private val journal: RecoveryJournal,
    /** Applies [releasedState]. Returns what the appliers reported; problems are shown, not hidden. */
    private val release: () -> ApplyOutcome,
    private val now: () -> Long = System::currentTimeMillis,
    private val verifierFor: (RecoveryMaterial) -> RecoveryVerifier = { RecoveryVerifier(it) },
) {

    /** Whether this device could ever accept a code. The screen asks before it prompts. */
    fun available(): Boolean = material()?.let { verifierFor(it).usable() } == true

    /** Whether enforcement is already released. A second code changes nothing, and says so. */
    fun released(): Boolean = mode.active()

    fun status(): LockoutStatus = lockout.status()

    fun submit(entered: String): RecoveryResult {
        val current = material()
            ?: return RecoveryResult.Unavailable(
                "this phone has not finished enrolling, so it has no recovery code yet"
            )
        val verifier = verifierFor(current)
        if (!verifier.usable()) {
            return RecoveryResult.Unavailable(
                "this phone was enrolled without recovery material; recover it from the console " +
                    "or re-enroll it"
            )
        }

        // Checked before anything is derived. A locked-out submission is not an attempt at the code
        // — nothing was verified — so it is not journalled either: a child holding the button down
        // would otherwise fill the pending queue with events that say nothing the lockout has not
        // already recorded.
        val gate = lockout.status()
        if (gate is LockoutStatus.Closed) return RecoveryResult.TooManyAttempts(gate.remainingMillis)

        if (!verifier.verify(entered)) {
            val next = lockout.recordFailure()
            journal.record(RecoveryAttempt(succeeded = false, occurredAtEpochMillis = now()))
            return RecoveryResult.Rejected(lockout.consecutiveFailures(), next)
        }

        lockout.recordSuccess()
        journal.record(RecoveryAttempt(succeeded = true, occurredAtEpochMillis = now()))
        mode.activate()
        return RecoveryResult.Released(release())
    }
}
