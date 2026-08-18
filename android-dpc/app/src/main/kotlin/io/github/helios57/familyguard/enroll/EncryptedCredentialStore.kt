package io.github.helios57.familyguard.enroll

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.json.Json

/**
 * [CredentialStore] backed by `EncryptedSharedPreferences` over a keystore-held master key.
 *
 * The device token is a bearer credential for this child's whole policy: it reads the schedule, the
 * app list and the child's identifier, and it writes usage back. On a device that is rooted later —
 * which is a thing a determined teenager does — plaintext preferences are a file to copy. The
 * keystore does not make that impossible, but it makes it require the device rather than a backup
 * of it.
 *
 * The whole record is stored under one key as JSON rather than as separate preferences, so a write
 * either lands or does not. Five separate `putString` calls have four states in between, and the
 * interesting one — a token stored with no server URL — is a device that can authenticate to
 * nothing.
 */
class EncryptedCredentialStore(context: Context) : CredentialStore {

    private val json = Json { ignoreUnknownKeys = true }

    // Lazily, because opening this touches the keystore and the constructor runs on whatever thread
    // built the store — including, at boot, the main one.
    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): Credentials? {
        val stored = preferences.getString(KEY, null) ?: return null
        // A record that will not parse is a record from a version that no longer exists, or a
        // corrupted one. Either way it cannot be used, and treating it as "not enrolled" lets the
        // device recover by enrolling again rather than crashing on every boot forever.
        return runCatching { json.decodeFromString(Credentials.serializer(), stored) }.getOrNull()
    }

    override fun save(credentials: Credentials) {
        preferences.edit()
            .putString(KEY, json.encodeToString(Credentials.serializer(), credentials))
            .commit()
    }

    override fun clear() {
        preferences.edit().remove(KEY).commit()
    }

    private companion object {
        const val FILE = "family-guard-credentials"
        const val KEY = "credentials"
    }
}
