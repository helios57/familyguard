package io.github.helios57.familyguard.energy

import android.net.TrafficStats
import android.os.Process
import android.os.SystemClock
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.concurrent.atomic.AtomicLong
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** What the meter reads from the platform. An interface so the arithmetic is tested without Android. */
interface EnergyClock {
    /** When this process started, on the wall clock. Fixed for the life of the process. */
    val processStartEpochMillis: Long

    /** `elapsedRealtime`: counts through sleep, cannot be moved. */
    fun elapsedMillis(): Long

    /** CPU time this process has used, in milliseconds. */
    fun cpuMillis(): Long

    /** Bytes this app's uid has received and sent since boot, or [UNSUPPORTED]. */
    fun rxBytes(): Long
    fun txBytes(): Long

    companion object {
        /** `TrafficStats.UNSUPPORTED`, repeated so this interface does not load an Android class. */
        const val UNSUPPORTED = -1L
    }
}

/**
 * What FamilyGuard spent on this phone since its process started (FR-26.5), sent with every heartbeat.
 *
 * Counters only grow while the process lives, and [since] names the process: the server turns two
 * reports with the same [since] into a difference and never subtracts across two different ones.
 */
@Serializable
data class EnergyReport(
    @SerialName("since") val since: String = "",
    @SerialName("cpu_ms") val cpuMs: Long = 0,
    @SerialName("rx_bytes") val rxBytes: Long = 0,
    @SerialName("tx_bytes") val txBytes: Long = 0,
    @SerialName("stream_opens") val streamOpens: Long = 0,
    @SerialName("events") val events: Long = 0,
    @SerialName("polls") val polls: Long = 0,
    @SerialName("pushes") val pushes: Long = 0,
    @SerialName("other_syncs") val otherSyncs: Long = 0,
    /** FR-26.1: time spent in each mode; null from a process that has not been told one. */
    @SerialName("active_ms") val activeMs: Long? = null,
    @SerialName("passive_ms") val passiveMs: Long? = null,
    /** FR-26.4: time the ad filter's tunnel ran each route; null from a process never told one. */
    @SerialName("route_full_ms") val routeFullMs: Long? = null,
    @SerialName("route_dns_ms") val routeDnsMs: Long? = null,
)

/**
 * Counts what FamilyGuard does that costs energy, and reads what it cost.
 *
 * One per process ([process]), not one per service: the connection service can be recreated inside a
 * process that keeps running, and a meter rebuilt with it would restart its counters under the same
 * [EnergyReport.since] — which the server would read as a phone that did less than it did.
 */
class EnergyMeter(private val clock: EnergyClock) {

    private val since: String = RFC3339.format(Instant.ofEpochMilli(clock.processStartEpochMillis))
    private val rxAtStart = clock.rxBytes()
    private val txAtStart = clock.txBytes()

    private val streamOpens = AtomicLong()
    private val events = AtomicLong()
    private val polls = AtomicLong()
    private val pushes = AtomicLong()
    private val otherSyncs = AtomicLong()

    /**
     * One sync, classified by the reason the connection service gives it: `wake:connected` is the
     * event stream (re)opening, any other `wake:` is an event on it, `poll` and `push` are the passive
     * mode's wake-ups (FR-26.2), and everything else — a start, an installed package, an alarm — is
     * counted as other.
     */
    fun countSync(why: String) {
        when {
            why == "wake:connected" -> streamOpens.incrementAndGet()
            why.startsWith("wake:") -> events.incrementAndGet()
            why.startsWith("poll") -> polls.incrementAndGet()
            why.startsWith("push") -> pushes.incrementAndGet()
            else -> otherSyncs.incrementAndGet()
        }
    }

    private var mode: io.github.helios57.familyguard.sync.PowerMode? = null
    private var modeSinceElapsed = 0L
    private var activeMs = 0L
    private var passiveMs = 0L

    /**
     * The phone is now in [mode] (FR-26.1). Time is counted on `elapsedRealtime`, so a phone asleep in
     * PASSIVE accrues PASSIVE time — which is the time this whole design is about.
     */
    @Synchronized
    fun modeChanged(mode: io.github.helios57.familyguard.sync.PowerMode) {
        if (mode == this.mode) return
        accrue(clock.elapsedMillis())
        this.mode = mode
    }

    @Synchronized
    private fun accrue(now: Long) {
        when (mode) {
            io.github.helios57.familyguard.sync.PowerMode.ACTIVE -> activeMs += now - modeSinceElapsed
            io.github.helios57.familyguard.sync.PowerMode.PASSIVE -> passiveMs += now - modeSinceElapsed
            null -> Unit
        }
        modeSinceElapsed = now
    }

    private var routeTold = false
    private var route: io.github.helios57.familyguard.filter.RouteMode? = null
    private var routeSinceElapsed = 0L
    private var routeFullMs = 0L
    private var routeDnsMs = 0L

    /** The ad filter's tunnel now runs [route], or none (null) (FR-26.4). */
    @Synchronized
    fun routeChanged(route: io.github.helios57.familyguard.filter.RouteMode?) {
        if (routeTold && route == this.route) return
        accrueRoute(clock.elapsedMillis())
        this.route = route
        routeTold = true
    }

    @Synchronized
    private fun accrueRoute(now: Long) {
        when (route) {
            io.github.helios57.familyguard.filter.RouteMode.FULL -> routeFullMs += now - routeSinceElapsed
            io.github.helios57.familyguard.filter.RouteMode.DNS_ONLY -> routeDnsMs += now - routeSinceElapsed
            null -> Unit
        }
        routeSinceElapsed = now
    }

    @Synchronized
    fun report(): EnergyReport {
        if (mode != null) accrue(clock.elapsedMillis())
        if (routeTold) accrueRoute(clock.elapsedMillis())
        return snapshot()
    }

    private fun snapshot(): EnergyReport = EnergyReport(
        since = since,
        cpuMs = clock.cpuMillis(),
        rxBytes = grown(rxAtStart, clock.rxBytes()),
        txBytes = grown(txAtStart, clock.txBytes()),
        streamOpens = streamOpens.get(),
        events = events.get(),
        polls = polls.get(),
        pushes = pushes.get(),
        otherSyncs = otherSyncs.get(),
        activeMs = if (mode != null) activeMs else null,
        passiveMs = if (mode != null) passiveMs else null,
        routeFullMs = if (routeTold) routeFullMs else null,
        routeDnsMs = if (routeTold) routeDnsMs else null,
    )

    /**
     * Bytes since the process started. A counter the platform does not support reads zero: the field
     * is required, the server refuses a negative one, and a phone without per-uid accounting is not
     * one this project supports (API 29+ always has it) — so this is the defensive branch only.
     */
    private fun grown(atStart: Long, now: Long): Long =
        if (atStart == EnergyClock.UNSUPPORTED || now == EnergyClock.UNSUPPORTED) 0 else maxOf(0, now - atStart)

    companion object {
        private val RFC3339: DateTimeFormatter =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss'Z'").withZone(ZoneOffset.UTC)

        /** This process's meter. */
        val process: EnergyMeter by lazy { EnergyMeter(AndroidEnergyClock) }
    }
}

/** The platform's answers for this process and this app's uid. */
object AndroidEnergyClock : EnergyClock {
    override val processStartEpochMillis: Long =
        System.currentTimeMillis() - (SystemClock.elapsedRealtime() - Process.getStartElapsedRealtime())

    override fun elapsedMillis(): Long = SystemClock.elapsedRealtime()
    override fun cpuMillis(): Long = Process.getElapsedCpuTime()
    override fun rxBytes(): Long = TrafficStats.getUidRxBytes(Process.myUid())
    override fun txBytes(): Long = TrafficStats.getUidTxBytes(Process.myUid())
}
