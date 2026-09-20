package io.github.helios57.familyguard.usage

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

/**
 * The undelivered sittings, in the same encrypted file as the day totals.
 *
 * The same file and a different key, unlike [EncryptedUsageStore]'s own reasoning about staying out
 * of the policy cache: these two are one measurement, they are written by the same poll, and a
 * phone that kept its totals but lost its sessions would show a parent a day whose timeline and
 * whose totals disagree.
 *
 * Encrypted for the reason the totals are, only more so: a minute-by-minute record of which app a
 * child had open is the most personal thing this product stores.
 */
class EncryptedSessionStore(context: Context) : SessionStore {

    private val json = Json { ignoreUnknownKeys = true }

    private val serializer = ListSerializer(UsageSession.serializer())

    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): List<UsageSession> {
        val stored = preferences.getString(KEY, null) ?: return emptyList()
        // Unparseable is treated as absent, exactly as the totals are: a device that throws on every
        // usage poll forever loses far more than the undelivered queue dropped here.
        return runCatching { json.decodeFromString(serializer, stored) }.getOrElse { emptyList() }
    }

    override fun save(sessions: List<UsageSession>) {
        // `commit`, not `apply`. Written from the sync path of a foreground service the platform may
        // stop at any moment, and an async write that had not landed would re-deliver sessions the
        // server already has — harmless, the server merges by start instant — or lose ones it does
        // not, which is not.
        preferences.edit().putString(KEY, json.encodeToString(serializer, sessions)).commit()
    }

    private companion object {
        const val FILE = "family-guard-usage"
        const val KEY = "sessions"
    }
}
