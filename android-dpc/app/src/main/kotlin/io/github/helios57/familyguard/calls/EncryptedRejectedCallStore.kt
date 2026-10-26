package io.github.helios57.familyguard.calls

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

/**
 * The undelivered rejected calls, encrypted like the sittings: who tried to reach a child is as
 * personal as what the child had open. Its own file, because the screening service writes it from a
 * process moment the connection service does not own.
 */
class EncryptedRejectedCallStore(context: Context) : RejectedCallStore {

    private val json = Json { ignoreUnknownKeys = true }

    private val serializer = ListSerializer(RejectedCall.serializer())

    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): List<RejectedCall> {
        val stored = preferences.getString(KEY, null) ?: return emptyList()
        return runCatching { json.decodeFromString(serializer, stored) }.getOrElse { emptyList() }
    }

    override fun save(calls: List<RejectedCall>) {
        // `commit`: a call rejected a moment before the process dies must still be reported.
        preferences.edit().putString(KEY, json.encodeToString(serializer, calls)).commit()
    }

    private companion object {
        const val FILE = "family-guard-calls"
        const val KEY = "rejected"
    }
}
