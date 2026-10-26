package io.github.helios57.familyguard.calls

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** One rejected call, waiting to be reported. [id] is what the server deduplicates a re-send on. */
@Serializable
data class RejectedCall(
    @SerialName("id") val id: String,
    @SerialName("number") val number: String,
    @SerialName("at_millis") val atMillis: Long,
)

interface RejectedCallStore {
    fun load(): List<RejectedCall>
    fun save(calls: List<RejectedCall>)
}

/**
 * The rejected calls the server does not have yet (FR-31.5).
 *
 * A rejected call is an event, like a sitting: it happens once and is re-sent until the server
 * acknowledges it, and only what was acknowledged leaves — a call rejected while an upload is in
 * flight stays for the next one. The id is minted here, once, so a re-send after a lost response
 * carries the same id and the server stores nothing new.
 *
 * Bounded at [MAX], dropping the oldest: a phone offline for a month under a call campaign must not
 * grow a preferences file without limit, and the newest calls are the ones a parent asks about.
 */
class RejectedCallLog(private val store: RejectedCallStore, private val newId: () -> String) {

    @Synchronized
    fun add(number: String, atMillis: Long) {
        store.save((store.load() + RejectedCall(newId(), number, atMillis)).takeLast(MAX))
    }

    @Synchronized
    fun pending(): List<RejectedCall> = store.load()

    @Synchronized
    fun acknowledge(ids: Collection<String>) {
        if (ids.isEmpty()) return
        val gone = ids.toSet()
        store.save(store.load().filterNot { it.id in gone })
    }

    companion object {
        const val MAX = 500
    }
}
