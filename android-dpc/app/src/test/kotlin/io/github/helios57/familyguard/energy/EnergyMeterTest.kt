package io.github.helios57.familyguard.energy

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-26.5: what this phone reports about the energy FamilyGuard spends. The server turns two of these
 * into a difference, so each counter must only ever grow while the process lives, and `since` must be
 * the same string for the whole process — a changed `since` is how the server knows counters reset.
 */
class EnergyMeterTest {

    private class FakeClock(
        var cpu: Long = 0,
        var rx: Long = 1_000,
        var tx: Long = 500,
    ) : EnergyClock {
        override val processStartEpochMillis = 1_790_000_000_000L
        var elapsed = 0L
        override fun elapsedMillis() = elapsed
        override fun cpuMillis() = cpu
        override fun rxBytes() = rx
        override fun txBytes() = tx
    }

    @Test
    fun `each sync is counted by what woke it`() {
        val meter = EnergyMeter(FakeClock())
        listOf("start", "wake:connected", "wake:connected", "wake:command", "wake:policy", "poll",
            "push", "package:com.example", "alarm").forEach(meter::countSync)
        val r = meter.report()
        assertEquals(2, r.streamOpens)
        assertEquals(2, r.events)
        assertEquals(1, r.polls)
        assertEquals(1, r.pushes)
        assertEquals(3, r.otherSyncs)
    }

    @Test
    fun `data is counted from when the process started, not since boot`() {
        val clock = FakeClock()
        val meter = EnergyMeter(clock)
        clock.rx = 1_000 + 4_096
        clock.tx = 500 + 100
        clock.cpu = 2_345
        val r = meter.report()
        assertEquals(4_096, r.rxBytes)
        assertEquals(100, r.txBytes)
        assertEquals(2_345, r.cpuMs)
    }

    @Test
    fun `an unsupported traffic counter reports zero rather than a negative number`() {
        val clock = FakeClock(rx = EnergyClock.UNSUPPORTED, tx = EnergyClock.UNSUPPORTED)
        val meter = EnergyMeter(clock)
        clock.rx = 5_000
        val r = meter.report()
        assertEquals(0, r.rxBytes)
        assertEquals(0, r.txBytes)
    }

    @Test
    fun `since is the process start, in UTC, the same on every report`() {
        val meter = EnergyMeter(FakeClock())
        assertEquals("2026-09-21T14:13:20Z", meter.report().since)
        assertEquals(meter.report().since, meter.report().since)
    }

    @Test
    fun `time in each mode is counted from the first mode it is told, and is null before that`() {
        val clock = FakeClock()
        val meter = EnergyMeter(clock)
        assertEquals("a build that never told the meter its mode must not claim zero", null, meter.report().activeMs)
        clock.elapsed = 1_000
        meter.modeChanged(io.github.helios57.familyguard.sync.PowerMode.ACTIVE)
        clock.elapsed = 61_000
        meter.modeChanged(io.github.helios57.familyguard.sync.PowerMode.PASSIVE)
        clock.elapsed = 361_000
        val r = meter.report()
        assertEquals(60_000L, r.activeMs)
        assertEquals("the mode still running counts up to now", 300_000L, r.passiveMs)
        meter.modeChanged(io.github.helios57.familyguard.sync.PowerMode.PASSIVE)
        clock.elapsed = 371_000
        assertEquals("telling it the same mode again changes nothing", 310_000L, meter.report().passiveMs)
    }

    /** FR-26.4: time in each filter route; no tunnel is neither, and a meter never told reports null. */
    @Test
    fun `time in each filter route is counted, and no tunnel counts as neither`() {
        val clock = FakeClock()
        val meter = EnergyMeter(clock)
        assertEquals(null, meter.report().routeFullMs)
        clock.elapsed = 1_000
        meter.routeChanged(io.github.helios57.familyguard.filter.RouteMode.FULL)
        clock.elapsed = 301_000
        meter.routeChanged(io.github.helios57.familyguard.filter.RouteMode.DNS_ONLY)
        clock.elapsed = 1_501_000
        meter.routeChanged(null)
        clock.elapsed = 2_000_000
        val r = meter.report()
        assertEquals(300_000L, r.routeFullMs)
        assertEquals(1_200_000L, r.routeDnsMs)
        assertEquals("the mode clock is separate", null, r.activeMs)
    }
}
