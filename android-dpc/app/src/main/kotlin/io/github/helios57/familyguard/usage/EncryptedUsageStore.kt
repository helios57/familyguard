package io.github.helios57.familyguard.usage

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.builtins.MapSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.Json

/**
 * The day totals, in their own encrypted preferences file.
 *
 * Its own file rather than a key in the policy cache's, so that clearing one cannot take the other:
 * a policy that fails to parse is dropped and re-fetched, and dropping the day totals with it would
 * silently hand back the quota a child has already spent.
 *
 * Encrypted for the same reason as the policy — a per-minute record of which apps a child used all
 * day is not something a lost phone should give up as a readable file.
 */
class EncryptedUsageStore(context: Context) : UsageStore {

    private val json = Json { ignoreUnknownKeys = true }

    private val serializer =
        MapSerializer(String.serializer(), MapSerializer(String.serializer(), Long.serializer()))

    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): Map<String, Map<String, Long>> {
        val stored = preferences.getString(KEY, null) ?: return emptyMap()
        // A record that will not parse is treated as absent. The alternative is a device that throws
        // on every usage poll forever, which loses far more than the one day that is dropped here.
        return runCatching { json.decodeFromString(serializer, stored) }.getOrElse { emptyMap() }
    }

    override fun save(totals: Map<String, Map<String, Long>>) {
        // `commit`, not `apply`: this is written from the sync path and the process is a foreground
        // service the platform may stop at any moment. An async write that had not landed would
        // restart the day's counter at zero, and the server's GREATEST upsert would then ignore
        // every report until it climbed back past what it had already been told.
        preferences.edit().putString(KEY, json.encodeToString(serializer, totals)).commit()
    }

    private companion object {
        const val FILE = "family-guard-usage"
        const val KEY = "totals"
    }
}
