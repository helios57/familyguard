package io.github.helios57.familyguard.sync

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.json.Json

/**
 * The last policy input the server sent, and the version this device has actually put into effect.
 *
 * This is what makes FR-9 possible. The phone must keep enforcing bedtime and the daily quota with
 * no network, and it cannot do that from a stored *desired state*: "bedtime starts in ten minutes"
 * is not a fact but a function of the clock, so what has to survive is the [Input] the engine
 * computes from, re-evaluated as the time moves.
 *
 * [appliedVersion] is deliberately separate from the version inside the stored input. The input is
 * what the server said; the applied version is what this device managed to make true. They differ
 * exactly when an apply failed, and that difference is the only thing standing between the console
 * and a phone it reports as up to date while three restrictions never took.
 */
interface PolicyCache {
    /** The last input the server sent, or null on a device that has never completed a sync. */
    fun load(): Input?

    /** Replaces it. Called *before* the state is applied, so a crash mid-apply still enforces. */
    fun save(input: Input)

    /** The policy version this device last applied with no problems. 0 when it never has. */
    fun appliedVersion(): Long

    /** Records a clean apply. Never called after an apply that reported a problem. */
    fun recordApplied(version: Long)

    /**
     * When this device last received a policy *from the server*, or 0 if it never has.
     *
     * Kept here rather than derived, because there is nothing to derive it from: a cached input
     * carries the instant the server built it, which is not the instant this phone received it, and
     * on a device that has been offline for a week those two differ by the week. It is the single
     * most useful line on the status screen — "last reached the family settings" is what tells a
     * parent whether the phone is enforcing something current or something from Tuesday — and it is
     * stamped on receipt rather than on apply, because a policy that arrived and then failed to
     * apply is still evidence the network and the credential work.
     */
    fun lastServerContact(): Long

    /** Stamps [lastServerContact]. Called only where a response actually came back. */
    fun recordServerContact(atEpochMillis: Long)

    /** Forgets all three. Only an un-enrollment should reach this. */
    fun clear()
}

/** [PolicyCache] in `EncryptedSharedPreferences`. See [encryptedPreferences] for why it is encrypted. */
class EncryptedPolicyCache(context: Context) : PolicyCache {

    private val json = Json { ignoreUnknownKeys = true }

    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): Input? {
        val stored = preferences.getString(KEY_INPUT, null) ?: return null
        // A record that will not parse came from a version of this app that no longer exists. It is
        // treated as absent rather than as a crash, so the device recovers on the next successful
        // sync instead of failing to start its connection service forever.
        return runCatching { json.decodeFromString(Input.serializer(), stored) }.getOrNull()
    }

    override fun save(input: Input) {
        preferences.edit()
            .putString(KEY_INPUT, json.encodeToString(Input.serializer(), input))
            .commit()
    }

    override fun appliedVersion(): Long = preferences.getLong(KEY_APPLIED, 0L)

    override fun lastServerContact(): Long = preferences.getLong(KEY_CONTACT, 0L)

    override fun recordServerContact(atEpochMillis: Long) {
        preferences.edit().putLong(KEY_CONTACT, atEpochMillis).commit()
    }

    override fun recordApplied(version: Long) {
        preferences.edit().putLong(KEY_APPLIED, version).commit()
    }

    override fun clear() {
        preferences.edit().remove(KEY_INPUT).remove(KEY_APPLIED).remove(KEY_CONTACT).commit()
    }

    private companion object {
        const val FILE = "family-guard-policy"
        const val KEY_INPUT = "input"
        const val KEY_APPLIED = "applied_version"
        const val KEY_CONTACT = "last_server_contact"
    }
}
