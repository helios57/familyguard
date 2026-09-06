package io.github.helios57.familyguard.recovery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The record that the control plane no longer accepts this phone's credential (FR-1.8).
 *
 * It exists because the condition is otherwise completely invisible from the phone. A revoked
 * device syncs, is answered 401, backs off and tries again — forever, quietly, with the child's
 * limits frozen at whatever they were. Nothing on the phone says so and nothing in the console
 * does either, because a device that cannot authenticate cannot report anything.
 *
 * Two properties carry the whole design, and both are about restraint:
 *
 * **Only a 401.** A notification that says "this phone is no longer linked" is the one notification
 * this app will ever ask a parent to act on. Raising it because one endpoint answered 404 is how
 * that notification becomes something people swipe away.
 *
 * **No re-stamping.** The recorded instant is what the screen reports as "unlinked since", and a
 * revoked phone retries every few minutes. Re-stamping would make a phone that has been cut off for
 * three days report that it happened just now.
 */
class LinkRefusedTest {

    /** Counts writes, because two of the properties below are "it does not write". */
    private class CountingStore(initial: Long? = null) : LinkRefusedStore {
        private var since: Long? = initial
        var writes: Int = 0
            private set

        override fun refusedSince(): Long? = since

        override fun setRefusedSince(epochMillis: Long?) {
            since = epochMillis
            writes++
        }
    }

    private var clock = FIRST
    private val store = CountingStore()
    private val refused = LinkRefused(store) { clock }

    @Test
    fun `a phone the server still accepts is not marked`() {
        assertFalse(refused.refused())
        assertNull(refused.refusedSince())
    }

    @Test
    fun `a 401 records when the link was refused`() {
        refused.recordRefusal(401)

        assertTrue(refused.refused())
        assertEquals(FIRST, refused.refusedSince())
    }

    @Test
    fun `no other status marks the phone`() {
        // Every one of these is non-retryable — `ApiException.retryable` is false for all of them —
        // so "the request failed permanently" is not the same statement as "this credential is
        // dead". A 500 is here too, as the retryable half of the same point.
        for (status in listOf(400, 403, 404, 409, 422, 500, 503)) {
            refused.recordRefusal(status)
            assertFalse("status $status marked the phone as unlinked", refused.refused())
        }
        assertEquals("nothing was written", 0, store.writes)

        // The positive control. Without it the loop above would pass just as well against a
        // `recordRefusal` that had been changed to do nothing at all.
        refused.recordRefusal(401)
        assertTrue(refused.refused())
    }

    @Test
    fun `the retry that follows does not move the start`() {
        refused.recordRefusal(401)
        clock = FIRST + 3 * 24 * 3_600_000L
        refused.recordRefusal(401)

        assertEquals(FIRST, refused.refusedSince())
        assertEquals("the second refusal wrote", 1, store.writes)
    }

    @Test
    fun `a server that answers again clears it`() {
        refused.recordRefusal(401)

        refused.clear()

        assertFalse(refused.refused())
        assertNull(refused.refusedSince())
    }

    @Test
    fun `clearing a phone that was never refused writes nothing`() {
        // Sync runs every few minutes on a healthy phone, and this is called on every one of them.
        refused.clear()
        refused.clear()

        assertEquals(0, store.writes)
        // The positive control for the count: the store does write when there is something to say.
        refused.recordRefusal(401)
        refused.clear()
        assertEquals(2, store.writes)
    }

    @Test
    fun `it survives the process, because the store does`() {
        // The notification has to come back after a reboot: the condition is not going to resolve
        // itself, and the phone can sit unlinked for days before anyone picks it up.
        val disk = InMemoryLinkRefusedStore()
        LinkRefused(disk) { FIRST }.recordRefusal(401)

        val afterRestart = LinkRefused(disk) { FIRST + 1_000 }

        assertTrue(afterRestart.refused())
        assertEquals(FIRST, afterRestart.refusedSince())
    }

    private companion object {
        /** 2026-09-06T02:01:18Z, the minute the first real phone was revoked by a mis-tap. */
        const val FIRST = 1_788_667_278_000L
    }
}
