package io.github.helios57.familyguard.agenda

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import java.time.LocalDateTime
import kotlinx.serialization.SerialName
import kotlinx.serialization.json.Json
import kotlinx.serialization.Serializable

/**
 * The agenda as the server sends it beside the policy (FR-24.3): today and tomorrow in the profile's
 * calendar, each with its holiday and its items in time order, already expanded from the entries.
 */
@Serializable
data class AgendaBlock(@SerialName("days") val days: List<AgendaDay> = emptyList())

@Serializable
data class AgendaDay(
    @SerialName("day") val day: String = "",
    @SerialName("holiday") val holiday: String = "",
    @SerialName("items") val items: List<AgendaItem> = emptyList(),
)

@Serializable
data class AgendaItem(
    @SerialName("entry_id") val entryId: String = "",
    @SerialName("title") val title: String = "",
    @SerialName("place") val place: String = "",
    @SerialName("starts_at") val startsAt: String = "",
    @SerialName("ends_at") val endsAt: String = "",
    @SerialName("optional") val optional: Boolean = false,
)

/** What the Heute screen shows: today and tomorrow, what is on now, and what comes next today. */
data class AgendaView(val today: AgendaDay?, val tomorrow: AgendaDay?, val current: AgendaItem?, val next: AgendaItem?)

/** FR-24.5: now and next, decided on the phone from its own clock. */
object AgendaNow {
    /**
     * Days are picked by date, never by position: a phone that stayed offline past midnight holds
     * yesterday's "today" and "tomorrow", and must show the second as today and nothing as tomorrow
     * rather than yesterday as today.
     */
    fun of(block: AgendaBlock, now: LocalDateTime): AgendaView {
        val date = now.toLocalDate()
        val today = block.days.firstOrNull { it.day == date.toString() }
        val tomorrow = block.days.firstOrNull { it.day == date.plusDays(1).toString() }
        val clock = now.toLocalTime().toString().take(5)
        val items = today?.items.orEmpty()
        val current = items.firstOrNull { it.startsAt <= clock && clock < it.endsAt }
        val next = items.firstOrNull { it.startsAt > clock }
        return AgendaView(today, tomorrow, current, next)
    }
}

/** Where the last agenda block is kept, so the Heute screen shows it offline. */
class EncryptedAgendaStore(context: Context) {
    private val json = Json { ignoreUnknownKeys = true }
    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    fun load(): AgendaBlock? {
        val stored = preferences.getString(KEY, null) ?: return null
        return runCatching { json.decodeFromString(AgendaBlock.serializer(), stored) }.getOrNull()
    }

    fun save(block: AgendaBlock) {
        preferences.edit().putString(KEY, json.encodeToString(AgendaBlock.serializer(), block)).commit()
    }

    private companion object {
        const val FILE = "family-guard-agenda"
        const val KEY = "agenda"
    }
}
