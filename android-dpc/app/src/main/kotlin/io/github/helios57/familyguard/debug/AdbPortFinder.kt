package io.github.helios57.familyguard.debug

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.util.Log
import java.net.InetAddress
import java.net.NetworkInterface
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * Finds the port this phone's own Wireless debugging listens on (FR-19.3).
 *
 * adbd picks a random port every time Wireless debugging is switched on, and announces it over
 * mDNS — `_adb-tls-connect._tcp` for adb itself and `_adb-tls-pairing._tcp` for the pairing
 * service while the pairing dialog is open. It writes the port nowhere an app can read: the system
 * property that holds it is not readable by an unprivileged app. So the phone does what a laptop
 * on the same Wi-Fi does, and asks the network — through [NsdManager], which also sees services the
 * phone announces itself. Shizuku reaches its own adbd the same way.
 *
 * Only an announcement whose address is one of THIS phone's addresses counts. On a home network the
 * parent's laptop or another phone may be announcing the same service type, and relaying a child's
 * debug session into somebody else's adbd is the one wrong answer this can give.
 */
class AdbPortFinder(context: Context) {

    private val nsd = context.applicationContext.getSystemService(NsdManager::class.java)

    enum class Service(val type: String) {
        CONNECT("_adb-tls-connect._tcp"),
        PAIRING("_adb-tls-pairing._tcp"),
    }

    data class Found(val address: InetAddress, val port: Int)

    /** The first announcement of [service] from this phone within [timeoutMillis], or null. */
    fun find(service: Service, timeoutMillis: Long = DEFAULT_TIMEOUT_MILLIS): Found? {
        val manager = nsd ?: return null
        val own = ownAddresses()
        if (own.isEmpty()) return null
        val candidates = LinkedBlockingQueue<NsdServiceInfo>()
        val started = CountDownLatch(1)
        var startFailed = false
        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) = started.countDown()
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.w(TAG, "mDNS discovery of $serviceType would not start: $errorCode")
                startFailed = true
                started.countDown()
            }
            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) = Unit
            override fun onDiscoveryStopped(serviceType: String) = Unit
            override fun onServiceFound(info: NsdServiceInfo) {
                candidates.offer(info)
            }
            override fun onServiceLost(info: NsdServiceInfo) = Unit
        }
        manager.discoverServices(service.type, NsdManager.PROTOCOL_DNS_SD, listener)
        try {
            started.await(2, TimeUnit.SECONDS)
            if (startFailed) return null
            val deadline = System.currentTimeMillis() + timeoutMillis
            while (true) {
                val left = deadline - System.currentTimeMillis()
                if (left <= 0) return null
                val next = candidates.poll(left, TimeUnit.MILLISECONDS) ?: return null
                // Resolved one at a time: NsdManager refuses a second resolve while one is running
                // (FAILURE_ALREADY_ACTIVE), and there are rarely more than one or two candidates.
                val resolved = resolve(manager, next, left) ?: continue
                if (resolved.address in own && resolved.port in 1..65535) return resolved
            }
        } finally {
            try {
                manager.stopServiceDiscovery(listener)
            } catch (ignored: IllegalArgumentException) {
                // Never started, which the start callback already reported.
            }
        }
    }

    @Suppress("DEPRECATION") // resolveService is deprecated from API 34 and still works; the
    // replacement does not exist below 34, and the floor is 29.
    private fun resolve(manager: NsdManager, info: NsdServiceInfo, timeoutMillis: Long): Found? {
        val result = LinkedBlockingQueue<Found>(1)
        val done = CountDownLatch(1)
        manager.resolveService(info, object : NsdManager.ResolveListener {
            override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                Log.d(TAG, "could not resolve ${serviceInfo.serviceName}: $errorCode")
                done.countDown()
            }
            override fun onServiceResolved(serviceInfo: NsdServiceInfo) {
                val host = serviceInfo.host
                if (host != null) result.offer(Found(host, serviceInfo.port))
                done.countDown()
            }
        })
        done.await(timeoutMillis.coerceAtMost(3_000), TimeUnit.MILLISECONDS)
        return result.poll()
    }

    private fun ownAddresses(): Set<InetAddress> = try {
        NetworkInterface.getNetworkInterfaces()?.toList().orEmpty()
            .filter { it.isUp }
            .flatMap { it.inetAddresses.toList() }
            .toSet()
    } catch (e: Exception) {
        emptySet()
    }

    companion object {
        private const val TAG = "FGDebug"
        const val DEFAULT_TIMEOUT_MILLIS = 5_000L
    }
}
