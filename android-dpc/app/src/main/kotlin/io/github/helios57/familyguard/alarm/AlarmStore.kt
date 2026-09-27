package io.github.helios57.familyguard.alarm

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.json.Json

/** Where the alarm rule the server last sent, and the phone's own ring state, are kept (FR-23.3). */
interface AlarmStore {
    fun schedule(): AlarmSchedule?
    fun saveSchedule(schedule: AlarmSchedule)
    fun state(): AlarmState
    fun saveState(state: AlarmState)
}

/**
 * [AlarmStore] in its own encrypted preferences file. Credential-encrypted like every other store
 * here, so it is readable after the first unlock — which is when BOOT_COMPLETED arrives and the next
 * ring is booked. A phone restarted overnight and never unlocked therefore books nothing until it is;
 * FR-23.3 says so rather than this pretending otherwise.
 */
class EncryptedAlarmStore(context: Context) : AlarmStore {
    private val json = Json { ignoreUnknownKeys = true }
    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun schedule(): AlarmSchedule? {
        val stored = preferences.getString(SCHEDULE, null) ?: return null
        return runCatching { json.decodeFromString(AlarmSchedule.serializer(), stored) }.getOrNull()
    }

    override fun saveSchedule(schedule: AlarmSchedule) {
        preferences.edit().putString(SCHEDULE, json.encodeToString(AlarmSchedule.serializer(), schedule)).commit()
    }

    override fun state(): AlarmState {
        val stored = preferences.getString(STATE, null) ?: return AlarmState()
        return runCatching { json.decodeFromString(AlarmState.serializer(), stored) }.getOrDefault(AlarmState())
    }

    override fun saveState(state: AlarmState) {
        preferences.edit().putString(STATE, json.encodeToString(AlarmState.serializer(), state)).commit()
    }

    private companion object {
        const val FILE = "family-guard-alarm"
        const val SCHEDULE = "schedule"
        const val STATE = "state"
    }
}
