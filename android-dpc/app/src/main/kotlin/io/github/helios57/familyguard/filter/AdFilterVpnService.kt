package io.github.helios57.familyguard.filter

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import io.github.helios57.familyguard.BuildConfig
import io.github.helios57.familyguard.R
import java.io.FileInputStream
import java.io.FileOutputStream
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.Socket
import java.util.concurrent.atomic.AtomicLong

/**
 * The tunnel. One file descriptor, one reader, one writer, and everything else already tested.
 *
 * This class is deliberately the thinnest part of the feature. Every decision it carries out was
 * taken somewhere that can be driven by a fixture — [TunnelPlan] decides whether to come up,
 * [PacketRouter] decides what happens to each packet, [TcpFlow] runs the connections, and
 * [TunnelWatchdog] decides when to give up. What is left here is the parts that need Android and
 * therefore cannot be unit-tested at all: a descriptor, two threads and a notification. Keeping
 * that set small is the only way any of it is checkable.
 *
 * ### Not being able to break the phone is a design constraint, not a wish
 *
 * A `VpnService` is the one thing this app installs that can take a child's internet away entirely,
 * so every lever that could do that is pinned:
 *
 * - **No lockdown.** `setAlwaysOnVpnPackage(admin, pkg, lockdownEnabled = false)`. With lockdown
 *   off, a tunnel that is down means traffic goes around it; with lockdown on, a tunnel that is
 *   down means no traffic at all, and recovering from that needs the console the phone can no
 *   longer reach.
 * - **This app is excluded from its own tunnel** ([addDisallowedApplication]). The sync connection
 *   is what switches the filter off again, so it must never be carried by the thing being switched
 *   off. Safe precisely because lockdown is off; under lockdown the same call is a hole.
 * - **`DISALLOW_CONFIG_VPN` is never applied.** A parent holding the phone can turn this off in
 *   Settings without the console, and that is on purpose.
 * - **The watchdog can stop it and keep it stopped**, because a tunnel that is up and carrying
 *   nothing is invisible to the platform. See [TunnelWatchdog].
 *
 * ### The one call that matters on every socket
 *
 * `protect()` exempts a socket from the tunnel's own routes. Without it, the filter's connection to
 * a destination would be routed back into the filter, forever. It fails silently — the connection
 * just never completes — so it is called in exactly one place ([NioUpstreamPool]) and a refusal
 * fails the connection there rather than becoming a hang out here.
 */
class AdFilterVpnService : VpnService() {

    private val connectivity by lazy { getSystemService(ConnectivityManager::class.java) }

    /** Everything that belongs to one run of the tunnel. Replaced wholesale, never patched. */
    private var live: Live? = null

    private val watchdog = TunnelWatchdog()

    @Volatile private var stoodDown = false

    /**
     * Resolvers seen on the networks **underneath** the tunnel, one entry per network.
     *
     * Watched by capability rather than read from the default network, because once this service is
     * running the default network IS the tunnel, and its resolver is the address this service
     * invented. Forwarding there would be a loop, and the symptom would be a phone on which no name
     * resolves at all.
     *
     * A second opinion, never the only one — see [upstreamNow]. This is what the callbacks have
     * said; that is what the platform says at the moment the decision is taken.
     */
    private val resolvers = ResolverBook<Network>()

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) {
            val before = upstreamNow()
            resolvers.learned(network, ResolverBook.usable(properties.dnsServers, TUNNEL_RESOLVER))
            reconsider(before, "the network's resolver changed")
        }

        override fun onLost(network: Network) {
            // This network's entry, and no other. Clearing the whole list here is what left the
            // tunnel standing down with "the network offers no resolver to forward queries to" on a
            // phone whose Wi-Fi had a resolver the entire time — see [ResolverBook].
            val before = upstreamNow()
            resolvers.lost(network)
            reconsider(before, "a network went away")
        }
    }

    /**
     * Where the tunnel would forward a query right now.
     *
     * Asked of the platform at the moment of the decision rather than remembered from the last
     * callback that happened to arrive. The remembered version had no repair path: once it was
     * empty — a callback that never fired, a process the platform restarted under an always-on VPN,
     * an unrelated network going away — nothing on the phone could refill it, and the filter was
     * off until somebody changed a network. A question that can be asked again cannot get stuck.
     *
     * The active network is skipped when it is the tunnel itself, which it is whenever this runs
     * with a tunnel up: its only resolver is the address this service invented.
     */
    private fun upstreamNow(): List<String> {
        val manager = connectivity
        val active = manager?.activeNetwork
        if (manager != null && active != null) {
            val capabilities = manager.getNetworkCapabilities(active)
            if (capabilities != null && capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) {
                val direct = ResolverBook.usable(
                    manager.getLinkProperties(active)?.dnsServers.orEmpty(), TUNNEL_RESOLVER,
                )
                if (direct.isNotEmpty()) return direct
            }
        }
        return resolvers.forwardTo(active)
    }

    /**
     * A network changed. Act only if it changed the answer.
     *
     * Three cases, and the middle one is the one that was missing: a tunnel that is up forwards to
     * whatever this last said, so a moved resolver has to reach it; a tunnel that is NOT up because
     * there was nowhere to forward has just been handed the one thing it was waiting for, and the
     * next sync is up to a minute away; and a change that leaves the answer identical is not an
     * event at all — the underlying link properties change every time a tunnel comes up.
     */
    private fun reconsider(before: List<String>, why: String) {
        val after = upstreamNow()
        if (after == before) return
        Log.i(TAG, "$why: ${after.size} resolver(s)")
        val running = live
        when {
            running == null -> if (after.isNotEmpty()) startTunnel()
            running.router != null -> restart(why)
        }
    }

    override fun onCreate() {
        super.onCreate()
        try {
            connectivity?.registerNetworkCallback(
                NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    // The capability every network that is NOT a VPN has. This is the whole reason
                    // a request is used instead of the default network.
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                    .build(),
                networkCallback,
            )
        } catch (e: Exception) {
            Log.w(TAG, "could not watch the underlying network: ${e.message}")
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Before anything that can block: the platform kills a service that has not called this
        // within five seconds of being started in the foreground.
        ServiceCompat.startForeground(
            this,
            NOTIFICATION_ID,
            notification(getString(R.string.filter_starting)),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            } else {
                0
            },
        )
        if (!BuildConfig.AD_FILTER_AVAILABLE) {
            Log.i(TAG, "this build does not carry the ad filter")
            stopSelf()
            return START_NOT_STICKY
        }
        when (intent?.action) {
            ACTION_STOP -> {
                stopTunnel("asked to stop")
                // Nothing to explain: a parent switched the filter off, and the console draws the
                // switch. "" is what clears the line; leaving the previous stand-down standing
                // would explain the absence of a tunnel nobody asked for.
                standReason = ""
                stopSelf()
                return START_NOT_STICKY
            }
            ACTION_POLICY_CHANGED -> {
                // Every sync sends this, changed or not. A tunnel already running the plan the
                // policy now asks for is left alone — see TunnelPlan.keeps for what a rebuild costs.
                if (keepsRunningTunnel()) return START_STICKY
                // A parent changed something. Whatever the watchdog concluded about the previous
                // configuration says nothing about this one.
                stoodDown = false
                watchdog.reset()
            }
        }
        startTunnel()
        // STICKY, so a service the platform killed for memory comes back. Coming back is safe
        // because coming up is [TunnelPlan]'s decision, taken again from scratch each time.
        return START_STICKY
    }

    /**
     * The user turned the tunnel off from Settings, or another VPN took over.
     *
     * Not fought. A parent holding the phone is allowed to do this, and an app that immediately
     * restarted the tunnel would be a phone nobody can get the filter off.
     */
    override fun onRevoke() {
        Log.i(TAG, "the tunnel was revoked")
        // Before stopTunnel, because that clears the flag this reads. A revoke that left the reason
        // at "" reported "not running" with nothing to explain it — the same empty answer as a
        // service that never started, which is the ambiguity FR-6.11 exists to remove.
        if (tunnelUp) standReason = getString(R.string.filter_off_revoked)
        stopTunnel("revoked")
        stopSelf()
    }

    override fun onDestroy() {
        // Only when a tunnel was actually up: if it was already down, whatever reason is standing
        // is the better explanation and must not be overwritten by the fact that the service is
        // now going away too.
        if (tunnelUp) standReason = getString(R.string.filter_off_service_stopped)
        stopTunnel("the service is going away")
        try {
            connectivity?.unregisterNetworkCallback(networkCallback)
        } catch (ignored: Exception) {
            // Never registered, or already gone. Nothing to do either way.
        }
        super.onDestroy()
    }

    override fun onBind(intent: Intent?) = super.onBind(intent)

    // ---- the tunnel --------------------------------------------------------------------------

    /** See TunnelPlan.keeps. Synchronized with [startTunnel], which is what replaces [live]. */
    @Synchronized
    private fun keepsRunningTunnel(): Boolean {
        val running = live ?: return false
        if (!tunnelUp) return false
        val state = FilterState.of(this)
        val next = TunnelPlan.decide(
            policy = state.policy,
            upstream = upstreamNow(),
            ruleCount = state.engine.ruleCount,
            stoodDown = false,
        )
        if (!TunnelPlan.keeps(running.run, next)) return false
        Log.d(TAG, "policy re-applied; the running tunnel already matches it")
        return true
    }

    @Synchronized
    private fun startTunnel() {
        stopTunnel("restarting")
        // FR-6.11. Said before anything that can take time, and overwritten by every branch below. A
        // heartbeat that lands while the tunnel is being built reads "starting", which is true —
        // not the "" a previous run left behind, which the console can only show as "has not
        // started on this phone".
        standReason = getString(R.string.filter_off_starting)
        val state = FilterState.of(this)
        val decision = TunnelPlan.decide(
            policy = state.policy,
            upstream = upstreamNow(),
            ruleCount = state.engine.ruleCount,
            stoodDown = stoodDown,
        )
        if (decision is TunnelDecision.Stand) {
            Log.i(TAG, "not running: ${decision.reason}")
            standReason = decision.reason
            note(getString(R.string.filter_off_because, decision.reason))
            return
        }
        val run = decision as TunnelDecision.Run

        val descriptor = try {
            establish(run.mode)
        } catch (e: Exception) {
            Log.w(TAG, "could not establish the tunnel: ${e.message}")
            null
        }
        if (descriptor == null) {
            // `establish` returns null when this app is not prepared as the VPN, which for a device
            // owner means always-on has not been set yet. Nothing to do but wait for the next sync.
            val why = getString(R.string.filter_not_permitted)
            standReason = why
            note(getString(R.string.filter_off_because, why))
            return
        }

        val live = Live(descriptor, run, state.engine)
        this.live = live
        if (!live.start()) {
            // The forwarder would not bind. Everything below this point claims a tunnel is up, and
            // it was run unconditionally: a filter that had stopped itself on the way up reported
            // "running" to the console and drew "Ad filter on" in the shade, which is the one
            // failure mode this whole report exists to prevent.
            this.live = null
            val why = getString(R.string.filter_off_no_forwarder)
            standReason = why
            note(getString(R.string.filter_off_because, why))
            return
        }
        tunnelUp = true
        // Cleared only here, where a tunnel is actually up. A reason left standing after the thing
        // it explained is over is how a console teaches a parent to ignore it (FR-6.11).
        standReason = ""
        watchdog.tunnelStarted(System.currentTimeMillis())
        note(getString(R.string.filter_running, state.engine.ruleCount))
        Log.i(TAG, "tunnel up: mode=${run.mode} rules=${state.engine.ruleCount}")
    }

    private fun establish(mode: RouteMode): ParcelFileDescriptor? {
        val builder = Builder()
            .setSession(getString(R.string.app_name))
            // Below the 1500 of a real link on purpose: every packet this writes travels inside the
            // phone's own connection, and one that needs fragmenting on the way out is one the peer
            // may never see. [TcpFlow.DEFAULT_MSS] is this number minus the headers.
            .setMtu(TUNNEL_MTU)
            .addAddress(TUNNEL_ADDRESS, TUNNEL_PREFIX)
            .addDnsServer(TUNNEL_RESOLVER)
            .setBlocking(true)
        when (mode) {
            // Only the invented resolver is routed. Everything else goes around the tunnel
            // untouched, so a defect in the connection machine can cost DNS and nothing else.
            RouteMode.DNS_ONLY -> builder.addRoute(TUNNEL_RESOLVER, 32)
            RouteMode.FULL -> builder.addRoute("0.0.0.0", 0)
        }
        // This app's own traffic must never depend on the tunnel: the sync connection is what can
        // switch the filter off again. Safe only because lockdown is off — under lockdown this same
        // call would be a bypass rather than a lifeline.
        try {
            builder.addDisallowedApplication(packageName)
        } catch (e: Exception) {
            Log.w(TAG, "could not exclude this app from its own tunnel: ${e.message}")
        }
        return builder.establish()
    }

    @Synchronized
    private fun stopTunnel(why: String) {
        // Ahead of the early return on purpose: the flag is what the heartbeat reports, and
        // "there is no tunnel" is exactly the state a caller that finds `live` already null is in.
        tunnelUp = false
        val going = live ?: return
        live = null
        Log.i(TAG, "tunnel down: $why")
        going.stop()
    }

    private fun restart(why: String) {
        Log.i(TAG, "restarting: $why")
        startTunnel()
    }

    /** One tick of the watchdog, called from the tunnel's own thread. */
    private fun observe(live: Live) {
        when (watchdog.sample(System.currentTimeMillis(), live.packetsIn.get(), live.packetsOut.get())) {
            TunnelWatchdog.Verdict.HEALTHY -> Unit
            TunnelWatchdog.Verdict.TEAR_DOWN -> restart("the tunnel carried nothing for a whole window")
            TunnelWatchdog.Verdict.STAND_DOWN -> {
                // Failing OFF. An advertisement is the cost of being wrong this way; a phone a
                // parent cannot fix is the cost of being wrong the other way.
                Log.w(TAG, "standing down: two windows with nothing carried")
                stoodDown = true
                stopTunnel("stood down by the watchdog")
                val why = getString(R.string.filter_carried_nothing)
                standReason = why
                note(getString(R.string.filter_off_because, why))
            }
        }
    }

    /**
     * One run of the tunnel: the descriptor, the threads reading and writing it, and the sockets
     * opened on its behalf.
     *
     * A class rather than a set of fields so that stopping is one call that cannot half-happen. The
     * previous run's reader holding the descriptor while the next run opens a new one is the defect
     * that ends with two tunnels racing for the same packets.
     */
    private inner class Live(
        private val descriptor: ParcelFileDescriptor,
        val run: TunnelDecision.Run,
        private val engine: FilterEngine,
    ) {
        val packetsIn = AtomicLong()
        val packetsOut = AtomicLong()

        private val input = FileInputStream(descriptor.fileDescriptor)
        private val output = FileOutputStream(descriptor.fileDescriptor)
        private val writeLock = Any()

        @Volatile private var running = true

        private val pool = NioUpstreamPool(
            protectStream = { socket: Socket -> protect(socket) },
            protectDatagram = { socket: DatagramSocket -> protect(socket) },
            log = { Log.d(TAG, it) },
        )

        private val forwarder = DnsForwarder(
            upstream = { run.upstream.mapNotNull { address -> runCatching { InetAddress.getByName(address) }.getOrNull() } },
            protect = { socket: DatagramSocket -> protect(socket) },
            log = { Log.d(TAG, it) },
        )

        var router: PacketRouter? = null
            private set

        private val reader = Thread(::pump, "familyguard-tunnel")

        /** True when the tunnel is carrying traffic. False means it stopped itself on the way up. */
        fun start(): Boolean {
            if (!forwarder.start()) {
                Log.w(TAG, "the DNS forwarder would not start; the tunnel is not safe to run")
                stop()
                return false
            }
            router = PacketRouter(
                engine = engine,
                dnsTunnel = DnsTunnel(engine, forwarder, ::write),
                opener = pool,
                toClient = ::write,
                mode = { run.mode },
                log = { Log.d(TAG, it) },
            )
            reader.isDaemon = true
            reader.start()
            return true
        }

        fun stop() {
            running = false
            router?.closeAll()
            // Closing the descriptor is what unblocks the reader: it is parked in a blocking read
            // on it, and there is no other way to interrupt that.
            try {
                descriptor.close()
            } catch (ignored: Exception) {
                // Already gone.
            }
            reader.join(2_000)
            pool.close()
            forwarder.close()
        }

        private fun write(packet: ByteArray) {
            synchronized(writeLock) {
                try {
                    output.write(packet)
                    packetsOut.incrementAndGet()
                } catch (e: Exception) {
                    if (running) Log.w(TAG, "could not write to the tunnel: ${e.message}")
                }
            }
        }

        private fun pump() {
            val buffer = ByteArray(TUNNEL_MTU)
            var lastHousekeeping = System.currentTimeMillis()
            while (running) {
                val length = try {
                    input.read(buffer)
                } catch (e: Exception) {
                    if (running) Log.w(TAG, "the tunnel read failed: ${e.message}")
                    -1
                }
                if (length <= 0) {
                    if (!running) return
                    // A zero-length read on a blocking descriptor means the far end is gone.
                    break
                }
                packetsIn.incrementAndGet()
                try {
                    router?.handle(buffer, length)
                } catch (e: Exception) {
                    // One malformed packet must never take the tunnel with it: the phone would lose
                    // its network over a byte an app sent.
                    Log.w(TAG, "a packet could not be handled: ${e.message}")
                }
                val now = System.currentTimeMillis()
                if (now - lastHousekeeping >= HOUSEKEEPING_MILLIS) {
                    lastHousekeeping = now
                    router?.expire()
                    observe(this)
                }
            }
            if (running) {
                // The descriptor died under us rather than being closed. Rebuild rather than sit
                // there holding a tunnel that carries nothing.
                Log.w(TAG, "the tunnel descriptor closed underneath the reader")
                restart("the descriptor closed")
            }
        }
    }

    // ---- the notification ------------------------------------------------------------------------

    private fun note(text: String) {
        val manager = getSystemService(NotificationManager::class.java) ?: return
        manager.notify(NOTIFICATION_ID, notification(text))
    }

    private fun notification(text: String): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        manager?.createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.filter_channel), NotificationManager.IMPORTANCE_MIN),
        )
        return NotificationCompat.Builder(this, CHANNEL)
            // A platform drawable, as every other notification in this app uses: there is no
            // custom icon in this project, and R.drawable.ic_notification does not exist.
            .setSmallIcon(android.R.drawable.ic_lock_idle_lock)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setOngoing(true)
            .setShowWhen(false)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .setVisibility(NotificationCompat.VISIBILITY_SECRET)
            .build()
    }

    companion object {
        private const val TAG = "FGFilter"

        const val ACTION_POLICY_CHANGED = "io.github.helios57.familyguard.AD_FILTER_POLICY_CHANGED"
        const val ACTION_STOP = "io.github.helios57.familyguard.AD_FILTER_STOP"

        /**
         * Inside 100.64.0.0/10, the shared address space reserved for carrier-grade NAT.
         *
         * Not 10.x or 192.168.x: those are what home networks use, and a tunnel address that
         * collides with the router's own subnet makes the phone unable to reach the printer while
         * the filter is on — a symptom no parent would connect to advertising.
         */
        const val TUNNEL_ADDRESS = "100.88.0.2"
        const val TUNNEL_PREFIX = 30
        const val TUNNEL_RESOLVER = "100.88.0.1"

        /** The link MTU, and [TcpFlow.DEFAULT_MSS] plus the 40 bytes of headers it leaves room for. */
        const val TUNNEL_MTU = 1400

        /** How often idle relays are reaped and the watchdog is asked. Driven by the reader. */
        const val HOUSEKEEPING_MILLIS = 15_000L

        private const val CHANNEL = "ad-filter"
        private const val NOTIFICATION_ID = 0x46474144

        /**
         * Whether a tunnel is up right now, for the heartbeat to report.
         *
         * A process-wide flag rather than a query, because there is nothing to query: a bound
         * `VpnService` is not enumerable, and `getAlwaysOnVpnPackage` answers what the platform was
         * *told*, which is the question the parent's switch already answers. This is the other one
         * — whether the tunnel actually came up — and the service is the only thing that knows it.
         *
         * Set false by [stopTunnel] and on `onDestroy`, so a process the platform killed reports
         * false the moment it comes back rather than inheriting a stale true.
         */
        @Volatile
        private var tunnelUp: Boolean = false

        /**
         * Why no tunnel is running, in the words [TunnelPlan] chose, for the console (FR-6.11).
         *
         * Three-valued, and the third value is the one this cost a day to learn. **null is "nothing
         * has been recorded"** — no decision has been taken since this process started — while ""
         * is "there is nothing to explain": a tunnel that is up, or a filter a parent switched off.
         *
         * It was initialised to "" and the two states were one. So a phone reporting
         * `running=false, reason=""` could mean the tunnel had stood down, or that the service had
         * never run at all, and those have opposite remedies. The family phone sat in exactly that
         * state on 0.6.11 — filter on, 180423 rules compiled, tunnel down, nothing said — and the
         * console had nothing to show but a guess.
         *
         * Beside [tunnelUp] rather than derived from it: *whether* a tunnel is up and *why* it is
         * not are different questions, and only the second one has a remedy attached.
         */
        @Volatile
        private var standReason: String? = null

        /** Null on a build where the filter cannot run at all — "not reported", not "off". */
        fun running(): Boolean? = if (BuildConfig.AD_FILTER_AVAILABLE) tunnelUp else null

        /**
         * Null on a build with no filter, and null on a build that has one and has recorded
         * nothing. [FilterReport] turns the second null into words — it is the one that knows
         * whether the filter is available at all.
         */
        fun reason(): String? = if (BuildConfig.AD_FILTER_AVAILABLE) standReason else null

        /**
         * Record a reason discovered OUTSIDE this service, for the console (FR-6.11).
         *
         * Only while no tunnel is up: a running tunnel is the answer to the question this text
         * exists to answer, and a stand-down written over it would tell a parent their working
         * filter is broken.
         */
        fun explain(reason: String) {
            if (!BuildConfig.AD_FILTER_AVAILABLE) return
            if (!tunnelUp) standReason = reason
        }

        /**
         * Bring the tunnel up, or re-decide whether it should be up. Safe to call repeatedly.
         *
         * The ask is RECORDED, and that is not bookkeeping: starting a service is asynchronous and
         * can fail in ways that never reach this process — a foreground start Android refuses, a
         * service the platform declines to bring up — and every one of those ends with a phone
         * whose filter is off and whose console has nothing to say. The sync path is the only place
         * that knows the filter was asked for at all, so it writes that down here, and whatever the
         * service decides milliseconds later overwrites it.
         */
        fun apply(context: Context) {
            if (!BuildConfig.AD_FILTER_AVAILABLE) return
            try {
                ContextCompat.startForegroundService(
                    context,
                    Intent(context, AdFilterVpnService::class.java).setAction(ACTION_POLICY_CHANGED),
                )
                // Only while nothing else explains the absence. What is left standing after the
                // service has had its chance is the case that had no words at all: asked, and never
                // heard from. Harmless if the service wins the race — a heartbeat with the tunnel
                // up reports no reason whatever this says.
                if (!tunnelUp && standReason.isNullOrEmpty()) {
                    standReason = context.getString(R.string.filter_off_not_started)
                }
            } catch (e: Exception) {
                // Android 12 and later refuse some background foreground-service starts outright,
                // and the throw is the only notice. Caught rather than propagated because the sync
                // that called this has a phone to finish configuring, and named rather than
                // swallowed because it is the answer to "why is the filter off".
                Log.w(TAG, "could not start the filter service: ${e.javaClass.simpleName}: ${e.message}")
                standReason = context.getString(R.string.filter_off_refused, e.javaClass.simpleName)
            }
        }

        fun stop(context: Context) {
            if (!BuildConfig.AD_FILTER_AVAILABLE) return
            context.startService(
                Intent(context, AdFilterVpnService::class.java).setAction(ACTION_STOP),
            )
        }
    }
}
