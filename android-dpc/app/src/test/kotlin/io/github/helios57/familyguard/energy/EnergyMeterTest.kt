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
}
