package io.github.helios57.familyguard.sync

import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * The lock every report, sync and update runs under (FR-3.2, FR-3.3).
 *
 * What it protects is not thread-safe on purpose — a usage tick drains the screen clock and moves
 * the window — so the property that matters is that two bodies never overlap, and that a body which
 * takes the lock again fails rather than hanging the connection loop.
 */
class SyncLockTest {

    @OptIn(ExperimentalCoroutinesApi::class) // advanceUntilIdle: the deterministic half of this file
    @Test
    fun `a second body waits until the first has finished`() = runTest {
        val lock = SyncLock()
        val release = CompletableDeferred<Unit>()
        val events = mutableListOf<String>()
        launch {
            lock.withLock {
                events += "first in"
                release.await()
                events += "first out"
            }
        }
        launch { lock.withLock { events += "second in" } }
        advanceUntilIdle()
        assertEquals("the second body ran while the first held the lock", listOf("first in"), events)

        release.complete(Unit)
        advanceUntilIdle()
        assertEquals(listOf("first in", "first out", "second in"), events)
    }

    /**
     * The production shape: the screen-off watcher, the poll and the sync each launch onto a
     * multi-threaded pool and switch to IO inside. Real threads, so an overlap is a real overlap.
     */
    @Test
    fun `bodies launched from many threads never overlap`() = runBlocking {
        val lock = SyncLock()
        val inside = AtomicInteger()
        val most = AtomicInteger()
        (1..64).map {
            async(Dispatchers.Default) {
                lock.withLock {
                    withContext(Dispatchers.IO) {
                        most.accumulateAndGet(inside.incrementAndGet(), ::maxOf)
                        delay(1)
                        inside.decrementAndGet()
                    }
                }
            }
        }.awaitAll()
        assertEquals("bodies ran at the same time under the sync lock", 1, most.get())
    }

    @Test
    fun `a body that throws lets the next one in`() = runTest {
        val lock = SyncLock()
        try {
            lock.withLock { throw IllegalStateException("a sync that failed") }
        } catch (_: IllegalStateException) {
        }
        var ran = false
        lock.withLock { ran = true }
        assertTrue("a failed body kept the lock, so nothing would ever sync again", ran)
    }

    /**
     * A nested take is a deadlock with a non-reentrant lock. It must be an exception, named, and it
     * must come at once — the timeout here is what a hang looks like, so a red from it is the bug.
     */
    @Test
    fun `taking the lock again from inside fails instead of waiting on itself`() = runBlocking {
        val lock = SyncLock()
        val failure = withTimeout(5_000) {
            runCatching { lock.withLock { withContext(Dispatchers.IO) { lock.withLock { } } } }
                .exceptionOrNull()
        }
        if (failure !is IllegalStateException) fail("a nested take did not fail; it was: $failure")
        assertTrue(failure!!.message.orEmpty().contains("not reentrant"))

        // And the lock is free again afterwards.
        var ran = false
        withTimeout(5_000) { lock.withLock { ran = true } }
        assertTrue(ran)
    }

    @Test
    fun `two separate locks do not count as re-entry`() = runTest {
        val outer = SyncLock()
        val inner = SyncLock()
        var ran = false
        outer.withLock { inner.withLock { ran = true } }
        assertTrue("one lock held made a different lock refuse", ran)
    }
}
