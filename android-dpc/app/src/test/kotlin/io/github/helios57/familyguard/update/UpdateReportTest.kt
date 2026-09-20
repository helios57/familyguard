package io.github.helios57.familyguard.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The record that carries a silent self-update failure to the console (FR-15.7).
 *
 * The behaviour under test is not "it stores a string" — it is that the string *goes away by
 * itself* once a newer build is running. Nothing on the success path knows this record exists: the
 * install kills the process, the new build starts from the same storage, and if clearing it were
 * somebody's responsibility then the console would show a stale failure under a phone that had
 * updated fine.
 */
class UpdateReportTest {

    private class Clock(var millis: Long = 1_000) : () -> Long {
        override fun invoke(): Long = millis
    }

    private fun report(store: UpdateReportStore, clock: Clock = Clock()) = UpdateReport(store, clock)

    @Test
    fun `reports nothing when nothing has failed`() {
        assertEquals("", report(InMemoryUpdateReportStore()).pending(runningVersionCode = 8))
    }

    @Test
    fun `reports the platform's own words while the same build is still running`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("Android asked for a tap that nobody can give", runningVersionCode = 8)

        assertEquals("Android asked for a tap that nobody can give", r.pending(runningVersionCode = 8))
        assertEquals(
            "reporting must not consume the record: the next heartbeat has to say it again",
            "Android asked for a tap that nobody can give", r.pending(runningVersionCode = 8),
        )
    }

    /** The whole point. A build above the one that failed is proof the failure is over. */
    @Test
    fun `clears itself once a newer build is running`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("the platform refused to stage this install", runningVersionCode = 8)

        assertEquals("", r.pending(runningVersionCode = 9))
        assertNull("a cleared report must not come back on the next boot", store.load())
    }

    /**
     * The negative control for the clause above, and the one that matters most: **zero is "the
     * package manager did not answer", not "newer than nothing".** Treating it as a version would
     * make every report vanish on exactly the devices whose state cannot be read — a control that
     * passes having evaluated nothing.
     */
    @Test
    fun `an unreadable version code does not clear the report`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("the platform refused to stage this install", runningVersionCode = 8)

        assertEquals("the platform refused to stage this install", r.pending(runningVersionCode = 0))
        assertEquals(
            "an older build than the one that failed is not progress either",
            "the platform refused to stage this install", r.pending(runningVersionCode = 7),
        )
    }

    @Test
    fun `the latest attempt is the one reported`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("the download failed (network is unreachable)", runningVersionCode = 8)
        r.record("the platform refused to stage this install", runningVersionCode = 8)

        assertEquals(
            "a device has one problem to report and it is the current one",
            "the platform refused to stage this install", r.pending(runningVersionCode = 8),
        )
    }

    @Test
    fun `a successful session drops the record without waiting for a version to be read`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("the platform refused to stage this install", runningVersionCode = 8)

        r.clear()
        assertNull(store.load())
        assertEquals("", r.pending(runningVersionCode = 8))
    }

    /**
     * **The hole the self-clearing rule cannot close, stated so that the fix for it has somewhere
     * to point.**
     *
     * [UpdateFailure] clears itself when a build ABOVE the recorded one is seen running, which
     * answers "it failed, then a later attempt worked". A failure recorded against the build that
     * is already the newest one has no such build coming: `pending` asks `running > from`, and
     * 18 > 18 is false on every heartbeat for the life of the install. So the line stays on the
     * parent's screen forever, on a phone with nothing wrong with it.
     *
     * Measured on the family phone 2026-09-20, where a lost connection during one `apk-info` call
     * put a permanent "This phone did not take the last update" on a phone running build 18 of 18.
     * `ConnectionService.updateCheck` now calls [UpdateReport.clear] when the server says this
     * phone is already current, which is the only other proof available that the failure is over.
     */
    @Test
    fun `a failure recorded against the newest build never clears itself, and only clear() ends it`() {
        val store = InMemoryUpdateReportStore()
        val r = report(store)
        r.record("the server did not say which build to install", runningVersionCode = 18)

        assertEquals(
            "a failure recorded against build 18 is still reported while build 18 runs",
            "the server did not say which build to install", r.pending(runningVersionCode = 18),
        )
        assertEquals(
            "and a thousand heartbeats later it is still reported, because nothing above 18 ever runs",
            "the server did not say which build to install", r.pending(runningVersionCode = 18),
        )

        r.clear()

        assertNull("clear() is the only thing that can end it", store.load())
        assertEquals("", r.pending(runningVersionCode = 18))
    }

    @Test
    fun `records when it happened, so the console can say how stale the failure is`() {
        val store = InMemoryUpdateReportStore()
        val clock = Clock(millis = 1_757_000_000_000)
        report(store, clock).record("  the platform refused to stage this install  ", runningVersionCode = 8)

        val failure = store.load() ?: throw AssertionError("nothing was recorded")
        assertEquals(1_757_000_000_000, failure.atEpochMillis)
        assertEquals(8L, failure.fromVersionCode)
        assertTrue("the reason is trimmed before it is stored", failure.reason.startsWith("the platform"))
    }
}
