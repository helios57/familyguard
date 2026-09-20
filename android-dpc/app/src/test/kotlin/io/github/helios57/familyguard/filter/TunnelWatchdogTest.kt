package io.github.helios57.familyguard.filter

import io.github.helios57.familyguard.filter.TunnelWatchdog.Verdict
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The "do not brick the phone" guarantee, asserted rather than hoped for.
 *
 * Every test here is one shape of a phone that has stopped working. The valuable half is not that
 * a broken tunnel is torn down — it is that a *quiet* tunnel, a *working* tunnel and a tunnel that
 * has only just come up are all left alone, because each of those looks identical to the broken one
 * if you only count packets and forget to ask how long for.
 */
class TunnelWatchdogTest {

    private val window = TunnelWatchdog.WINDOW_MILLIS
    private val busy = TunnelWatchdog.MINIMUM_PACKETS * 2

    @Test
    fun `nothing is decided before a whole window has passed`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        // A tunnel that has carried nothing at all, one millisecond short of the window.
        val verdict = watchdog.sample(now = window - 1, packetsIn = busy, packetsOut = 0)

        assertEquals(Verdict.HEALTHY, verdict)
        assertEquals(0, watchdog.strikes())
    }

    @Test
    fun `a quiet phone is not a broken tunnel`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        // A pocket: a handful of keepalives went in, nothing came back, for ten whole windows.
        var verdict = Verdict.HEALTHY
        for (window in 1..10) {
            verdict = watchdog.sample(
                now = window * this.window,
                packetsIn = window * 3L,
                packetsOut = 0,
            )
        }

        assertEquals(Verdict.HEALTHY, verdict)
        assertEquals(0, watchdog.strikes())
    }

    @Test
    fun `a tunnel that answers is healthy however lopsided the counts`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        // A thousand packets in and one back is a perfectly ordinary bulk upload.
        val verdict = watchdog.sample(now = window, packetsIn = 1_000, packetsOut = 1)

        assertEquals(Verdict.HEALTHY, verdict)
    }

    @Test
    fun `a busy window that carried nothing back tears the tunnel down`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        val verdict = watchdog.sample(now = window, packetsIn = busy, packetsOut = 0)

        assertEquals(Verdict.TEAR_DOWN, verdict)
        assertEquals(1, watchdog.strikes())
    }

    @Test
    fun `a second broken window stands the tunnel down for good`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        assertEquals(Verdict.TEAR_DOWN, watchdog.sample(window, packetsIn = busy, packetsOut = 0))
        // What the service does between the two: throws the router away and starts a new one, whose
        // counters begin at zero.
        watchdog.tunnelStarted(now = window)
        val verdict = watchdog.sample(now = 2 * window, packetsIn = busy, packetsOut = 0)

        assertEquals(Verdict.STAND_DOWN, verdict)
        assertEquals(2, watchdog.strikes())
    }

    @Test
    fun `restarting the tunnel does not forgive the strike that caused it`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)
        watchdog.sample(window, packetsIn = busy, packetsOut = 0)

        watchdog.tunnelStarted(now = window)

        // The whole point of the second strike is that it survives the restart. If `tunnelStarted`
        // cleared it, the phone would rebuild a broken tunnel forever and never stand down.
        assertEquals(1, watchdog.strikes())
    }

    @Test
    fun `one good window between two bad ones clears the count`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)

        assertEquals(Verdict.TEAR_DOWN, watchdog.sample(window, packetsIn = busy, packetsOut = 0))
        assertEquals(Verdict.HEALTHY, watchdog.sample(2 * window, packetsIn = 3 * busy, packetsOut = busy))
        val verdict = watchdog.sample(3 * window, packetsIn = 5 * busy, packetsOut = busy)

        // A tear-down, not a stand-down: two failures a day apart are two first failures.
        assertEquals(Verdict.TEAR_DOWN, verdict)
        assertEquals(1, watchdog.strikes())
    }

    @Test
    fun `a policy change forgives everything`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)
        watchdog.sample(window, packetsIn = busy, packetsOut = 0)
        watchdog.tunnelStarted(now = window)
        watchdog.sample(2 * window, packetsIn = busy, packetsOut = 0)
        assertEquals(2, watchdog.strikes())

        watchdog.reset()

        assertEquals(0, watchdog.strikes())
        // And the window is forgotten too, so the first sample after a reset cannot inherit a
        // verdict from a window that started before the parent changed anything.
        watchdog.tunnelStarted(now = 3 * window)
        assertEquals(
            Verdict.TEAR_DOWN,
            watchdog.sample(4 * window, packetsIn = busy, packetsOut = 0),
        )
    }

    @Test
    fun `sampling without a start adopts the counts instead of judging them`() {
        val watchdog = TunnelWatchdog()

        // No `tunnelStarted`. The counters are already high because the tunnel has been up a while;
        // read as a window, that is "a lot arrived and nothing came back" and would stand it down
        // on the first observation.
        val verdict = watchdog.sample(now = 10 * window, packetsIn = 100_000, packetsOut = 0)

        assertEquals(Verdict.HEALTHY, verdict)
        assertEquals(0, watchdog.strikes())
    }

    @Test
    fun `counts are read as deltas, not totals`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0, packetsIn = 1_000_000, packetsOut = 500_000)

        // Nothing moved at all this window, but both totals are enormous.
        val verdict = watchdog.sample(window, packetsIn = 1_000_000, packetsOut = 500_000)

        assertEquals(Verdict.HEALTHY, verdict)
    }

    @Test
    fun `the window restarts on every verdict, so one busy window is not judged twice`() {
        val watchdog = TunnelWatchdog()
        watchdog.tunnelStarted(now = 0)
        assertEquals(Verdict.TEAR_DOWN, watchdog.sample(window, packetsIn = busy, packetsOut = 0))

        // Same totals, one window later: nothing new arrived, so there is nothing to judge.
        val verdict = watchdog.sample(2 * window, packetsIn = busy, packetsOut = 0)

        assertEquals(Verdict.HEALTHY, verdict)
        assertEquals(0, watchdog.strikes())
    }

    @Test
    fun `a stricter limit stands down on the first failure`() {
        val watchdog = TunnelWatchdog(strikesBeforeStandDown = 1)
        watchdog.tunnelStarted(now = 0)

        assertEquals(Verdict.STAND_DOWN, watchdog.sample(window, packetsIn = busy, packetsOut = 0))
    }
}
