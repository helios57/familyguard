package io.github.helios57.familyguard.recovery

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

/**
 * The three pieces of recovery state that must survive a reboot, in one encrypted file.
 *
 * One file rather than three, because they are one concern and clearing part of it is never right:
 * a lockout without its journal is a device that punished someone and cannot say so, and a journal
 * without its lockout is a record of attempts that are no longer being counted. It is separate from
 * the credential file and the policy cache for the reason `encryptedPreferences` gives — an
 * un-enrollment drops the credential, and it must not silently take the record of a recovery too.
 *
 * Encrypted for the ordinary reason, and for one specific to this: the lockout is the only thing
 * between a shoulder-surfed code and an unmanaged phone, and a plaintext file that can be rewritten
 * to `{"consecutive_failures":0}` between attempts is not a lockout.
 *
 * **`commit`, not `apply`, on every write.** A lockout that had not reached disk when the process
 * was killed is a lockout a power button defeats, and the process being killed is the expected
 * behaviour of the thing being defended against.
 *
 * Three properties rather than one class implementing all three interfaces: `LockoutStore.load()`
 * and `RecoveryJournalStore.load()` differ only in return type, which is a conflicting overload in
 * Kotlin. Splitting them also keeps each caller holding exactly the interface it uses.
 */
class AndroidRecoveryStore(context: Context) {

    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    val lockout: LockoutStore = object : LockoutStore {
        override fun load(): LockoutState {
            val stored = preferences.getString(KEY_LOCKOUT, null) ?: return LockoutState()
            // A record that will not parse is read as a *fresh* lockout, not an absent one: the
            // fields are a count and a deadline, and no reading of "corrupt" should hand back
            // attempts. It costs a parent nothing, because the count only matters once it is high.
            return runCatching { JSON.decodeFromString(LockoutState.serializer(), stored) }
                .getOrElse { LockoutState() }
        }

        override fun save(state: LockoutState) {
            preferences.edit()
                .putString(KEY_LOCKOUT, JSON.encodeToString(LockoutState.serializer(), state))
                .commit()
        }
    }

    val mode: RecoveryModeStore = object : RecoveryModeStore {
        override fun activeSince(): Long? =
            preferences.getLong(KEY_ACTIVE_SINCE, ABSENT).takeIf { it != ABSENT }

        override fun setActiveSince(epochMillis: Long?) {
            val editor = preferences.edit()
            if (epochMillis == null) {
                editor.remove(KEY_ACTIVE_SINCE)
            } else {
                editor.putLong(KEY_ACTIVE_SINCE, epochMillis)
            }
            editor.commit()
        }
    }

    val journal: RecoveryJournalStore = object : RecoveryJournalStore {
        override fun load(): List<RecoveryAttempt> {
            val stored = preferences.getString(KEY_JOURNAL, null) ?: return emptyList()
            // Unparseable here means *lost records*, and an empty list is the honest answer: there
            // is nothing left to report. Unlike the lockout, guessing a value would invent events.
            return runCatching { JSON.decodeFromString(ATTEMPTS, stored) }.getOrElse { emptyList() }
        }

        override fun save(attempts: List<RecoveryAttempt>) {
            preferences.edit().putString(KEY_JOURNAL, JSON.encodeToString(ATTEMPTS, attempts)).commit()
        }
    }

    private companion object {
        const val FILE = "family-guard-recovery"
        const val KEY_LOCKOUT = "lockout"
        const val KEY_ACTIVE_SINCE = "active_since"
        const val KEY_JOURNAL = "journal"

        /** `getLong` needs a default and 0 is a legal instant. Nothing is ever stored at Long.MIN. */
        const val ABSENT = Long.MIN_VALUE

        val JSON = Json { ignoreUnknownKeys = true; encodeDefaults = true }
        val ATTEMPTS = ListSerializer(RecoveryAttempt.serializer())
    }
}
