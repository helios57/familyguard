package io.github.helios57.familyguard.sync

import kotlin.coroutines.AbstractCoroutineContextElement
import kotlin.coroutines.CoroutineContext
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext

/**
 * One device-touching body at a time: a sync, a cached re-enforcement, an update, a usage report.
 *
 * Every one of those is read-decide-write against something that is not thread-safe — the platform
 * an applier plans against, or the usage tracker, ledger and reporter a report drains — so two of
 * them interleaved plan against a half-changed device or credit one window twice (FR-3.2, FR-3.3).
 *
 * Two things on top of a bare `Mutex`, each for a failure that was silent:
 *
 * - **[Held] is the proof a body is inside.** A function that must only run with the lock held
 *   takes one, so calling it from a bare `scope.launch` is a compile error rather than a race. The
 *   screen-off report was exactly that call until 2026-10-01, and it looked like every other report.
 * - **Re-entry fails loudly.** The lock is not reentrant, and a body that takes it a second time
 *   would wait on itself forever — the service keeps its notification, the stream stays open, and
 *   nothing syncs again. Throwing names the bug at its first occurrence instead.
 */
class SyncLock {

    /** Handed to a body running inside [withLock]; only this class can make one. */
    sealed interface Held

    private object Token : Held

    /** Marks the coroutines running inside this lock, so a nested [withLock] is caught. */
    private class Inside(val lock: SyncLock) : AbstractCoroutineContextElement(Key) {
        companion object Key : CoroutineContext.Key<Inside>
    }

    /** One permit: one body at a time. */
    private val permits = Semaphore(PERMITS)

    suspend fun <T> withLock(block: suspend (Held) -> T): T {
        check(currentCoroutineContext()[Inside]?.lock !== this) {
            "SyncLock taken again by a body that already holds it; it is not reentrant, so this " +
                "would have waited on itself forever"
        }
        return permits.withPermit { withContext(Inside(this)) { block(Token) } }
    }

    private companion object {
        const val PERMITS = 1
    }
}
