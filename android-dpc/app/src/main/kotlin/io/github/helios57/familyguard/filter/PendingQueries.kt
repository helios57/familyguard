package io.github.helios57.familyguard.filter

/**
 * The queries this device has sent upstream and is still waiting for.
 *
 * Every app on the phone picks its own DNS transaction ids, and they collide constantly — two apps
 * starting at the same moment routinely both pick a small number. Forwarding them unchanged over
 * one shared socket means the first answer to arrive is handed to whichever app is looked up first,
 * and that app receives an address for a name it never asked about. So each query gets an id this
 * class owns, and the original is restored on the way back.
 *
 * ### Bounded on purpose
 *
 * [capacity] is a hard ceiling and a full table simply refuses. The failure it prevents is the one
 * that has no symptom until it is fatal: a resolver that stops answering, an app that retries in a
 * loop, and a map on the tunnel thread that grows until the process is killed — hours later, with
 * the crash pointing at whatever allocated last. Refusing means the query is not relayed, the app
 * retries, and the phone behaves exactly as it does on a bad network.
 */
class PendingQueries(
    private val clock: () -> Long,
    private val timeoutMillis: Long = 5_000,
    private val capacity: Int = 1024,
) {

    /** One outstanding query. */
    class Pending(val originalId: Int, val sentAt: Long, val onAnswer: (ByteArray) -> Unit)

    private val outstanding = HashMap<Int, Pending>()
    private var nextId = 0

    val size: Int get() = outstanding.size

    /**
     * Claim an id for a query whose own id is [originalId].
     *
     * Returns the id to put on the wire, or `null` when the table is full.
     */
    fun register(originalId: Int, onAnswer: (ByteArray) -> Unit): Int? {
        if (outstanding.size >= capacity) return null
        // At most one full sweep of the id space; with a bounded table this cannot spin.
        for (attempt in 0 until ID_SPACE) {
            val candidate = nextId
            nextId = (nextId + 1) and 0xFFFF
            if (!outstanding.containsKey(candidate)) {
                outstanding[candidate] = Pending(originalId, clock(), onAnswer)
                return candidate
            }
        }
        return null
    }

    /** The query [ourId] belongs to, removed from the table, or null if nothing is waiting on it. */
    fun complete(ourId: Int): Pending? = outstanding.remove(ourId)

    /**
     * Drop everything older than the timeout, and say how many went.
     *
     * A dropped query is never answered. That is the same thing the app sees when a packet is lost,
     * which is a case every resolver client already handles — unlike a synthesised failure, which
     * some cache.
     */
    fun expire(): Int {
        val cutoff = clock() - timeoutMillis
        val iterator = outstanding.entries.iterator()
        var dropped = 0
        while (iterator.hasNext()) {
            if (iterator.next().value.sentAt <= cutoff) {
                iterator.remove()
                dropped++
            }
        }
        return dropped
    }

    fun clear() = outstanding.clear()

    private companion object {
        const val ID_SPACE = 65536
    }
}
