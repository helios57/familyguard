package io.github.helios57.familyguard.sync

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import androidx.core.content.ContextCompat

/**
 * Lets through at most one position per [minIntervalMillis] (FR-27.2). GPS and the network provider
 * both answer during Live, and each at its own pace; the server needs one position every ten seconds,
 * not every answer.
 */
class LiveThrottle(private val minIntervalMillis: Long) {
    private var lastSent: Long? = null

    @Synchronized
    fun take(atElapsed: Long): Boolean {
        val last = lastSent
        if (last != null && atElapsed - last < minIntervalMillis) return false
        lastSent = atElapsed
        return true
    }

    @Synchronized
    fun reset() {
        lastSent = null
    }
}

/**
 * The phone's position every [intervalMillis] while Live runs (FR-27.2), handed to [report].
 *
 * GPS for the walk home, the network provider beside it because GPS indoors or in a pocket may say
 * nothing for minutes. Both are asked at the interval; [LiveThrottle] sends one. Needs the location
 * permissions the device owner grants itself, and a foreground service of type `location` — without
 * that type Android throttles a background app to a few positions an hour.
 */
class LiveLocation(
    private val context: Context,
    private val intervalMillis: Long = INTERVAL_MILLIS,
    private val report: (Location) -> Unit,
) {
    private val throttle = LiveThrottle(intervalMillis - 2_000)
    private var running = false
    private val listener = LocationListener { location ->
        if (throttle.take(SystemClock.elapsedRealtime())) report(location)
    }

    /** Idempotent. Returns whether updates are now being received from at least one provider. */
    @Synchronized
    fun start(): Boolean {
        if (running) return true
        val manager = context.getSystemService(LocationManager::class.java) ?: return false
        val fine = ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_FINE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED
        if (!fine) {
            Log.w(TAG, "Live: no location permission, so no positions")
            return false
        }
        throttle.reset()
        var any = false
        for (provider in listOf(LocationManager.GPS_PROVIDER, LocationManager.NETWORK_PROVIDER)) {
            if (!runCatching { manager.isProviderEnabled(provider) }.getOrDefault(false)) continue
            runCatching {
                manager.requestLocationUpdates(provider, intervalMillis, 0f, listener, Looper.getMainLooper())
            }.onSuccess { any = true }
                .onFailure { Log.w(TAG, "Live: $provider refused: ${it.message}") }
        }
        running = any
        Log.i(TAG, if (any) "Live: positions every ${intervalMillis / 1000} s" else "Live: no provider is on")
        return any
    }

    @Synchronized
    fun stop() {
        if (!running) return
        running = false
        runCatching { context.getSystemService(LocationManager::class.java)?.removeUpdates(listener) }
        Log.i(TAG, "Live: positions stopped")
    }

    companion object {
        private const val TAG = "FamilyGuard/Live"
        const val INTERVAL_MILLIS = 10_000L
    }
}
