package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.enforce.DesiredState

/**
 * Whether this device is currently released by a recovery code, and since when.
 *
 * A nullable instant rather than a boolean plus a timestamp: two fields that must agree is two
 * fields that can disagree, and "released since never" is not a state this should be able to hold.
 */
interface RecoveryModeStore {
    fun activeSince(): Long?
    fun setActiveSince(epochMillis: Long?)
}

/** A [RecoveryModeStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryRecoveryModeStore(initial: Long? = null) : RecoveryModeStore {
    private var since: Long? = initial
    override fun activeSince(): Long? = since
    override fun setActiveSince(epochMillis: Long?) {
        since = epochMillis
    }
}

/**
 * The released state (FR-12.2), and the flag that keeps it in effect until the control plane is
 * reached again.
 *
 * **Persisted, and deliberately with no expiry.** A parent who recovers a phone in a car park has
 * no way to know when it will next see the server, and a recovery that quietly re-enforced itself
 * twenty minutes later would be the same brick with a delay. It ends exactly when
 * `Synchronizer.applyFrom` runs with a policy that came from the server — which is also the moment
 * the parent can see, on the console, that the device is managed again.
 *
 * The obvious alternative — clear it when the *device* next reaches the network — is wrong for a
 * reason worth writing down: a phone can have a connection and still be refused by the server, and
 * a refusal is exactly when the device must stay released.
 */
class RecoveryMode(
    private val store: RecoveryModeStore,
    private val now: () -> Long = System::currentTimeMillis,
) {
    fun active(): Boolean = store.activeSince() != null

    fun activeSince(): Long? = store.activeSince()

    fun activate() {
        // Not re-stamped: a second code entered while already released must not move the start,
        // because the start is what the console reports as "unmanaged since".
        if (store.activeSince() == null) store.setActiveSince(now())
    }

    /** Called when a policy from the server has been applied. Idempotent. */
    fun clear() {
        if (store.activeSince() != null) store.setActiveSince(null)
    }
}

/**
 * What a recovered device enforces: nothing.
 *
 * `DesiredState()` is not a shortcut for "empty" — every field of it defaults to the value that
 * means *not enforced*, and `ReleasedStateTest` asserts exactly that by encoding this object with
 * `encodeDefaults` and refusing any field that is not `false`, `0`, `""` or `[]`. A future field
 * whose neutral value is `true` will fail that test rather than silently ship a recovery that
 * leaves one restriction standing.
 *
 * Two things this does *not* do, both of them deliberate:
 *
 * **It does not dismiss the keyguard.** There is no platform call that does, for a device owner on
 * API 26 or later — see `LockManager`. What `locked = false` achieves is that the standing parent
 * lock stops re-asserting itself, so the phone stops re-locking every sync and the device PIN gets
 * the person in. A recovery that claimed to clear a keyguard it cannot touch would be a lie told
 * to somebody who is already having a bad day.
 *
 * **It does not un-enroll.** The credential stays, the service keeps running, and the next
 * successful sync takes the phone straight back under management. Recovery is a release, not a
 * removal: the parent gets their child's phone working again without losing the enrollment they
 * would otherwise have to redo from a factory reset.
 */
fun releasedState(): DesiredState = DesiredState()
