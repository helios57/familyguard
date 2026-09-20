package io.github.helios57.familyguard.usage

import kotlinx.serialization.Serializable

/**
 * One sitting, queued for delivery: what ran, from when, to when (FR-3.7).
 *
 * Wall-clock milliseconds, because they are the platform's own timestamps and the server stores
 * instants. [ForegroundSpan] is the same three fields — this type exists because it is *persisted*,
 * and a serialised shape that is also the type the fold produces would make every change to the fold
 * a change to a file already written on a phone.
 */
@Serializable
data class UsageSession(
    val packageName: String,
    val startMillis: Long,
    val endMillis: Long,
) {
    val durationMillis: Long get() = (endMillis - startMillis).coerceAtLeast(0)
}

/** Where the queue of undelivered sessions survives a process death. */
interface SessionStore {
    fun load(): List<UsageSession>
    fun save(sessions: List<UsageSession>)
}

/** A [SessionStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemorySessionStore(initial: List<UsageSession> = emptyList()) : SessionStore {
    private var state: List<UsageSession> = initial
    override fun load(): List<UsageSession> = state
    override fun save(sessions: List<UsageSession>) {
        state = sessions
    }
}

/**
 * The sittings this phone has measured and not yet delivered.
 *
 * ### Why a queue at all, when the day totals are cumulative
 *
 * [UsageLedger] can afford to be stateless about delivery: it holds a running TOTAL, the server
 * merges with `GREATEST`, and a report that never arrives is repaired by the next one, which carries
 * the same number plus whatever came after. A session is the opposite — it is an event, it happens
 * once, and a report that does not arrive is simply gone. So this keeps them until the server has
 * said it has them.
 *
 * ### What is dropped, and why dropping is right
 *
 * - **Anything shorter than [MIN_DURATION_MILLIS].** Opening an app crosses two or three activities
 *   and the platform reports each; sub-second spans are those transitions, not sittings. They also
 *   round to zero seconds on the wire, so a timeline drawing them would draw slivers with no width
 *   and no meaning.
 * - **Anything older than [RETAIN_MILLIS].** The server refuses a session older than its backfill
 *   window, so holding one past that is holding something that can never be delivered.
 * - **The OLDEST, when [capacity] is reached.** A phone that cannot reach the network for a week
 *   must not grow this file without bound, and when something has to go it is the part a parent is
 *   least likely to be looking at — and the part closest to being refused as too old anyway.
 *
 * Every one of those is a silent loss by design, which is why [dropped] counts them: a queue that
 * quietly throws work away and cannot say so is indistinguishable from a phone that measured
 * nothing.
 */
class SessionLog(
    private val store: SessionStore,
    private val capacity: Int = DEFAULT_CAPACITY,
    private val retainMillis: Long = RETAIN_MILLIS,
) {

    private val queue: ArrayDeque<UsageSession> = ArrayDeque(store.load())

    private var droppedCount: Long = 0

    /** How many sessions this log has thrown away since the process started. */
    fun dropped(): Long = droppedCount

    /** How many are waiting to be delivered. */
    fun pending(): Int = queue.size

    /**
     * Adds the sessions one window closed.
     *
     * [nowMillis] is the wall clock, used only to decide what is too old to keep. Spans are stored
     * with the platform's own timestamps and are never clamped to the window that reported them:
     * a session seeded from an earlier poll carries its TRUE start, which is the whole reason this
     * record is worth keeping separately from the day totals.
     */
    fun record(spans: List<ForegroundSpan>, nowMillis: Long) {
        var added = false
        for (span in spans) {
            if (span.packageName.isBlank()) continue
            if (span.durationMillis < MIN_DURATION_MILLIS) {
                droppedCount++
                continue
            }
            queue.addLast(UsageSession(span.packageName, span.startMillis, span.endMillis))
            added = true
        }
        if (!added) return
        prune(nowMillis)
        store.save(queue.toList())
    }

    /** The next [limit] sessions to deliver, oldest first. */
    fun batch(limit: Int): List<UsageSession> = queue.take(limit.coerceAtLeast(0))

    /**
     * Forgets sessions the server has accepted.
     *
     * By identity rather than by count: a `record` can run between the send and the acknowledgement
     * — the sync loop and the usage poll are the same thread today, but nothing in the types says
     * so — and dropping "the first n" would then discard sessions that were never sent.
     */
    fun acknowledge(sent: Collection<UsageSession>) {
        if (sent.isEmpty()) return
        val gone = sent.toHashSet()
        if (queue.removeAll(gone)) store.save(queue.toList())
    }

    /** Forgets everything. Only an un-enrollment should reach this. */
    fun clear() {
        if (queue.isEmpty()) return
        queue.clear()
        store.save(emptyList())
    }

    private fun prune(nowMillis: Long) {
        val oldest = nowMillis - retainMillis
        while (queue.isNotEmpty() && queue.first().endMillis < oldest) {
            queue.removeFirst()
            droppedCount++
        }
        while (queue.size > capacity) {
            queue.removeFirst()
            droppedCount++
        }
    }

    companion object {
        /**
         * Below this a span is an app transition rather than a sitting. One second, because the
         * server reports whole seconds and anything under one of them arrives as a session with no
         * duration at all.
         */
        const val MIN_DURATION_MILLIS = 1_000L

        /** The server's own backfill window. Past it a session can never be delivered. */
        const val RETAIN_MILLIS = 7L * 24 * 60 * 60 * 1000

        /**
         * About a week of a real child's phone. Measured on the family phone on 2026-09-20: 43
         * packages and 99 minutes in a day, which is a few hundred sittings — so this is the bound
         * on a phone that cannot reach the network for as long as the sessions could be delivered
         * for anyway.
         */
        const val DEFAULT_CAPACITY = 2_000
    }
}
