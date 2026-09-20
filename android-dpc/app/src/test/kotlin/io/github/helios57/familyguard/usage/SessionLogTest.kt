package io.github.helios57.familyguard.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The queue of sittings waiting to reach the server (FR-3.7).
 *
 * A sitting is an **event**: it happens once, and one that is dropped before the server has it is
 * simply gone. The day totals can afford to be careless about delivery because they are cumulative
 * and the server merges them with `GREATEST`; this cannot, and every test here is about a way the
 * carelessness would be invisible.
 */
class SessionLogTest {

    private val store = InMemorySessionStore()

    @Test
    fun `a recorded span is queued with the times the platform reported`() {
        val log = SessionLog(store)

        log.record(listOf(span(GAME, 10_000L, 70_000L)), nowMillis = 70_000L)

        assertEquals(
            listOf(UsageSession(GAME, 10_000L, 70_000L)),
            log.batch(10),
        )
    }

    /**
     * The whole reason this is a separate collaborator from [UsageLedger]. The tracker clamps a
     * carried span to the window it is crediting; a *record* of what ran when must keep the true
     * start, or a two-hour film reads as twenty-four five-minute sittings.
     */
    @Test
    fun `a span that started before the window keeps its true start`() {
        val log = SessionLog(store)

        log.record(listOf(span(GAME, 1_000L, 3_600_000L)), nowMillis = 3_600_000L)

        assertEquals(1_000L, log.batch(10).single().startMillis)
    }

    /** Opening an app crosses several activities; the platform reports each as its own span. */
    @Test
    fun `a sub-second span is an app transition and is dropped, countably`() {
        val log = SessionLog(store)

        log.record(listOf(span(GAME, 10_000L, 10_400L)), nowMillis = 10_400L)

        assertTrue(log.batch(10).isEmpty())
        assertEquals(1L, log.dropped())
    }

    @Test
    fun `a span with no package name is not queued`() {
        val log = SessionLog(store)

        log.record(listOf(span("", 10_000L, 70_000L)), nowMillis = 70_000L)

        assertTrue(log.batch(10).isEmpty())
    }

    /** The loss the whole class exists for: a process death must not take the queue with it. */
    @Test
    fun `the queue survives the process`() {
        SessionLog(store).record(listOf(span(GAME, 10_000L, 70_000L)), nowMillis = 70_000L)

        val afterRestart = SessionLog(store)

        assertEquals(listOf(UsageSession(GAME, 10_000L, 70_000L)), afterRestart.batch(10))
    }

    @Test
    fun `an acknowledged session is forgotten, on this process and the next`() {
        val log = SessionLog(store)
        log.record(listOf(span(GAME, 10_000L, 70_000L), span(BOOK, 80_000L, 140_000L)), nowMillis = 140_000L)

        log.acknowledge(log.batch(1))

        assertEquals(listOf(UsageSession(BOOK, 80_000L, 140_000L)), log.batch(10))
        assertEquals(listOf(UsageSession(BOOK, 80_000L, 140_000L)), SessionLog(store).batch(10))
    }

    /**
     * Acknowledging by identity rather than by count, and the case where the two differ.
     *
     * "Forget the first n" agrees with this whenever the batch is still a prefix of the queue, which
     * it usually is — so the cheap version of this test passes against either implementation and
     * proves nothing. The queue moves under a send in one way that is not a prefix: a `record`
     * between the send and the acknowledgement can PRUNE, dropping the very sittings that were
     * sent, and then "the first n" eats n sittings that were never delivered. Here that is B, a
     * sitting the server has never seen and would never be offered again.
     */
    @Test
    fun `a prune between the send and the acknowledgement does not eat an undelivered sitting`() {
        val log = SessionLog(store, capacity = 2)
        log.record(listOf(span(GAME, 0L, 60_000L)), nowMillis = 60_000L)
        log.record(listOf(span(BOOK, 100_000L, 160_000L)), nowMillis = 160_000L)
        val sent = log.batch(1)
        assertEquals(listOf(UsageSession(GAME, 0L, 60_000L)), sent)

        // Over capacity: the oldest goes, and the oldest is exactly what was just sent.
        log.record(listOf(span(MUSIC, 200_000L, 260_000L)), nowMillis = 260_000L)
        log.acknowledge(sent)

        assertEquals(
            listOf(
                UsageSession(BOOK, 100_000L, 160_000L),
                UsageSession(MUSIC, 200_000L, 260_000L),
            ),
            log.batch(10),
        )
    }

    /** Past the server's backfill window a session can never be delivered, so holding it is waste. */
    @Test
    fun `a session older than the retention window is dropped`() {
        val log = SessionLog(store)
        val week = SessionLog.RETAIN_MILLIS

        log.record(listOf(span(GAME, 1_000L, 59_000L)), nowMillis = 59_000L)
        log.record(listOf(span(BOOK, week, week + 60_000L)), nowMillis = week + 60_000L)

        assertEquals(listOf(UsageSession(BOOK, week, week + 60_000L)), log.batch(10))
        assertEquals(1L, log.dropped())
    }

    /** A phone off the network for a week must not grow this file without bound. */
    @Test
    fun `the oldest is dropped when the queue is full`() {
        val log = SessionLog(store, capacity = 2)

        repeat(4) { log.record(listOf(span(GAME, it * 100_000L, it * 100_000L + 60_000L)), nowMillis = 400_000L) }

        assertEquals(listOf(200_000L, 300_000L), log.batch(10).map { it.startMillis })
        assertEquals(2L, log.dropped())
    }

    /**
     * A queue that throws work away and cannot say so is indistinguishable from a phone that
     * measured nothing, which is the difference between "your child used nothing" and "we lost it".
     */
    @Test
    fun `nothing is dropped on the happy path`() {
        val log = SessionLog(store)

        log.record(listOf(span(GAME, 10_000L, 70_000L)), nowMillis = 70_000L)

        assertEquals(0L, log.dropped())
        assertEquals(1, log.pending())
    }

    @Test
    fun `batch never hands over more than it is asked for`() {
        val log = SessionLog(store)
        repeat(5) { log.record(listOf(span(GAME, it * 100_000L, it * 100_000L + 60_000L)), nowMillis = 500_000L) }

        assertEquals(2, log.batch(2).size)
        assertEquals(5, log.batch(99).size)
        assertTrue(log.batch(0).isEmpty())
    }

    @Test
    fun `clearing forgets everything, including on disk`() {
        val log = SessionLog(store)
        log.record(listOf(span(GAME, 10_000L, 70_000L)), nowMillis = 70_000L)

        log.clear()

        assertEquals(0, log.pending())
        assertEquals(0, SessionLog(store).pending())
    }

    /**
     * Two runs of the same app are two sittings. They differ by their start, which is also what the
     * server's primary key is made of — so a log that folded them would be a log that made the
     * server's idempotency silently lossy.
     */
    @Test
    fun `two sittings of the same app are two rows`() {
        val log = SessionLog(store)

        log.record(
            listOf(span(GAME, 10_000L, 70_000L), span(GAME, 200_000L, 260_000L)),
            nowMillis = 260_000L,
        )

        val queued = log.batch(10)
        assertEquals(2, queued.size)
        assertNotEquals(queued[0].startMillis, queued[1].startMillis)
    }

    private fun span(pkg: String, from: Long, to: Long) = ForegroundSpan(pkg, from, to)

    private companion object {
        const val GAME = "com.example.game"
        const val BOOK = "com.example.book"
        const val MUSIC = "com.example.music"
    }
}
