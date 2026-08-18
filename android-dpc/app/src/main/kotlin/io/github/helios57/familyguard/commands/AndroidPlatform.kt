package io.github.helios57.familyguard.commands

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.media.AudioAttributes
import android.media.AudioManager
import android.media.Ringtone
import android.media.RingtoneManager
import android.os.Build
import android.os.CancellationSignal
import android.os.Handler
import android.os.Looper
import android.os.VibrationAttributes
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager
import androidx.core.content.ContextCompat
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * [SirenDevice] over the platform's alarm stream, ringtone player and vibrator.
 *
 * Thin on purpose: every decision — the auto-stop cap, what is captured, what is restored — is in
 * [SirenController], which has JVM tests. What is left here is the four calls, and each of them is
 * the one that cannot be exercised without a phone.
 */
class AndroidSirenDevice(private val context: Context) : SirenDevice {

    private val audio: AudioManager? = context.getSystemService(AudioManager::class.java)

    private val ringtone = AtomicReference<Ringtone?>(null)

    override fun startTone() {
        // Stopped first: `start()` is only reached when the controller believes nothing is playing,
        // and an orphaned Ringtone from a killed-and-restarted service would otherwise be a tone
        // with no handle to stop it.
        stopTone()
        val uri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_ALARM)
            ?: RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE)
            ?: throw IllegalStateException("this device has no default alarm or ringtone sound")
        val tone = RingtoneManager.getRingtone(context, uri)
            ?: throw IllegalStateException("the platform would not open the alarm sound")
        // USAGE_ALARM is what routes this to the alarm stream, which is the stream a silenced ringer
        // does not silence. Setting the volume without setting this would raise a stream nothing is
        // playing on.
        tone.audioAttributes = AudioAttributes.Builder()
            .setUsage(AudioAttributes.USAGE_ALARM)
            .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
            .build()
        tone.isLooping = true
        ringtone.set(tone)
        tone.play()
    }

    override fun stopTone() {
        ringtone.getAndSet(null)?.let { if (it.isPlaying) it.stop() }
    }

    override fun startVibration() {
        val vibrator = vibrator() ?: throw IllegalStateException("this device has no vibrator")
        // 0 repeats from index 0, i.e. forever — the controller's cap and `STOP_ALARM` are what end
        // it. A one-shot would stop after a second and leave a phone that is only findable by ear.
        val effect = VibrationEffect.createWaveform(PATTERN, 0)
        // The attributes are what make this survive Do Not Disturb's vibration setting and the
        // platform's own "stop vibrating for a background app" rules, so both branches declare
        // ALARM. `VibrationAttributes` is the supported form from API 33; below that the only way to
        // say the same thing is the AudioAttributes overload, which is deprecated but is what exists
        // on this app's minSdk.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            vibrator.vibrate(effect, VibrationAttributes.createForUsage(VibrationAttributes.USAGE_ALARM))
        } else {
            @Suppress("DEPRECATION") // the VibrationAttributes overload is API 33; minSdk here is 29
            vibrator.vibrate(
                effect,
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_ALARM)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .build(),
            )
        }
    }

    override fun stopVibration() {
        vibrator()?.cancel()
    }

    override fun alarmVolume(): Int? = audio?.getStreamVolume(AudioManager.STREAM_ALARM)

    override fun maxAlarmVolume(): Int =
        audio?.getStreamMaxVolume(AudioManager.STREAM_ALARM)
            ?: throw IllegalStateException("this device has no audio service")

    override fun setAlarmVolume(level: Int) {
        val manager = audio ?: throw IllegalStateException("this device has no audio service")
        manager.setStreamVolume(AudioManager.STREAM_ALARM, level, 0)
    }

    private fun vibrator(): Vibrator? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            context.getSystemService(VibratorManager::class.java)?.defaultVibrator
        } else {
            @Suppress("DEPRECATION") // the VibratorManager route is API 31; minSdk here is 29
            context.getSystemService(Vibrator::class.java)
        }

    private companion object {
        /** Off, on, off — a pulse rather than a drone, which carries further through a cushion. */
        val PATTERN = longArrayOf(0, 800, 400)
    }
}

/**
 * [SirenTimer] on the main looper.
 *
 * The main thread and not a coroutine scope: the auto-stop has to survive the coroutine that started
 * it being cancelled — which is exactly what happens when the event stream drops while a siren is
 * ringing, and it is the case the cap exists for.
 */
class HandlerSirenTimer(
    private val handler: Handler = Handler(Looper.getMainLooper()),
) : SirenTimer {

    private val pending = AtomicReference<Runnable?>(null)

    override fun arm(delayMillis: Long, action: () -> Unit) {
        cancel()
        val runnable = Runnable { action() }
        pending.set(runnable)
        handler.postDelayed(runnable, delayMillis)
    }

    override fun cancel() {
        pending.getAndSet(null)?.let { handler.removeCallbacks(it) }
    }
}

/**
 * [LocationSource] over `LocationManager`.
 *
 * `LocationManager` rather than Play Services' fused provider: this APK is installed from a
 * provisioning QR onto a fully managed phone, and a dependency on Google Play Services is a
 * dependency on a component that may be absent, out of date, or disabled on exactly the hardware a
 * family buys cheaply. The platform manager is always there.
 */
class AndroidLocationSource(private val context: Context) : LocationSource {

    private val manager: LocationManager? = context.getSystemService(LocationManager::class.java)

    override fun permitted(): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_FINE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED ||
            ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_COARSE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    override fun freshFix(timeoutMillis: Long): Fix? {
        val locations = manager ?: return null
        if (!locations.isProviderEnabled(LocationManager.GPS_PROVIDER)) return null
        val latch = CountDownLatch(1)
        val result = AtomicReference<Location?>(null)

        // API 30 gives a single-shot request that releases the hardware itself; on 29 the same shape
        // has to be built out of a listener that is removed by hand. Both are wrapped in the same
        // latch so the caller blocks once and for the same budget either way.
        val cancel = CancellationSignal()
        val listener = LocationListener { location ->
            result.set(location)
            latch.countDown()
        }
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                locations.getCurrentLocation(
                    LocationManager.GPS_PROVIDER,
                    cancel,
                    context.mainExecutor,
                ) { location ->
                    result.set(location)
                    latch.countDown()
                }
            } else {
                @Suppress("DEPRECATION") // getCurrentLocation is API 30; minSdk here is 29
                locations.requestLocationUpdates(
                    LocationManager.GPS_PROVIDER,
                    0L,
                    0f,
                    listener,
                    Looper.getMainLooper(),
                )
            }
            latch.await(timeoutMillis, TimeUnit.MILLISECONDS)
        } catch (_: SecurityException) {
            // The permission was revoked between `permitted()` and here. Reported as "no fresh fix",
            // which falls through to the last known position — and if there is none, to an honest
            // "no position", never to a fabricated one.
            return null
        } finally {
            // Both paths, unconditionally: a request left running is the GNSS receiver left on, and
            // FR-9 says the hardware is released.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                cancel.cancel()
            } else {
                runCatching { locations.removeUpdates(listener) }
            }
        }
        return result.get()?.toFix()
    }

    override fun lastKnownFix(): Fix? {
        val locations = manager ?: return null
        // The newest across every provider, not GPS's. A network fix from ten minutes ago is worth
        // more to a parent than a GPS fix from yesterday, and picking one provider by name is how a
        // phone that has been indoors all day reports nothing.
        return try {
            locations.allProviders
                .mapNotNull { runCatching { locations.getLastKnownLocation(it) }.getOrNull() }
                .maxByOrNull { it.time }
                ?.toFix()
        } catch (_: SecurityException) {
            null
        }
    }

    private fun Location.toFix() = Fix(
        latitude = latitude,
        longitude = longitude,
        accuracyM = if (hasAccuracy()) accuracy.toDouble() else null,
        // The platform's own instant for this fix, which for a cached one is minutes or hours ago.
        // See `Fix`: re-dating it here is the one thing that would make the field a lie.
        capturedAtEpochMillis = time,
    )
}
