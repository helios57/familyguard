package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * Keeping one shared socket's answers apart, and refusing to grow without limit.
 *
 * Both failures here are the kind that only appear on a phone, hours in: two apps that happened to
 * pick the same transaction id, or a resolver that stopped answering while an app retried in a
 * loop. Neither has a symptom until it is either a wrong answer or a dead process.
 */
class PendingQueriesTest {

    private var now = 1_000L
    private val queries = PendingQueries(clock = { now }, timeoutMillis = 5_000, capacity = 4)

    @Test
    fun `two apps that picked the same id get their own answers`() {
        val answers = mutableListOf<String>()
        val first = queries.register(0x0001) { answers += "first" }!!
        val second = queries.register(0x0001) { answers += "second" }!!

        assertNotEquals("the wire ids differ even though the apps' do not", first, second)

        queries.complete(second)!!.onAnswer(ByteArray(0))

        assertEquals(listOf("second"), answers)
        assertEquals("the other one is still waiting", 1, queries.size)
    }

    @Test
    fun `the original id comes back with the answer`() {
        val ours = queries.register(0xBEEF) {}!!

        assertEquals(0xBEEF, queries.complete(ours)!!.originalId)
    }

    @Test
    fun `an answer to nothing is nothing`() {
        assertNull(queries.complete(7))
    }

    @Test
    fun `an answer is only delivered once`() {
        val ours = queries.register(1) {}!!

        assertEquals(1, queries.complete(ours)?.originalId)
        assertNull("a duplicate answer finds an empty table", queries.complete(ours))
    }

    /**
     * The failure with no symptom until it is fatal: a resolver stops answering, an app retries in
     * a loop, and the table grows until the process is killed hours later.
     */
    @Test
    fun `a full table refuses rather than growing`() {
        repeat(4) { assertNotEquals(null, queries.register(it) {}) }

        assertNull("the fifth is refused, not queued", queries.register(5) {})
        assertEquals(4, queries.size)
    }

    @Test
    fun `refusing is temporary — a completed query frees its slot`() {
        val ids = (0 until 4).map { queries.register(it) {}!! }
        assertNull(queries.register(99) {})

        queries.complete(ids[0])

        assertNotEquals(null, queries.register(99) {})
    }

    @Test
    fun `a query nobody answered is dropped, and its callback never runs`() {
        var ran = false
        queries.register(1) { ran = true }

        now += 5_001

        assertEquals(1, queries.expire())
        assertEquals(0, queries.size)
        assertEquals("dropped is not answered", false, ran)
    }

    @Test
    fun `a query still inside the timeout survives the sweep`() {
        queries.register(1) {}

        now += 4_999

        assertEquals(0, queries.expire())
        assertEquals(1, queries.size)
    }

    @Test
    fun `the sweep frees the slots it dropped`() {
        repeat(4) { queries.register(it) {} }
        now += 6_000
        queries.expire()

        assertNotEquals(null, queries.register(9) {})
    }
}
