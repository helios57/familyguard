package io.github.helios57.familyguard.alarm

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.AudioAttributes
import android.media.AudioManager
import android.media.MediaPlayer
import android.media.RingtoneManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager
import android.util.Log
import androidx.core.app.NotificationCompat
import io.github.helios57.familyguard.R
import java.time.Instant

/**
 * The ringing alarm clock (FR-23.4): the alarm ringtone on the alarm stream, vibration, and a
 * notification whose full-screen intent puts [AlarmActivity] over the lock screen.
 *
 * A foreground service rather than the activity alone, because the sound must not depend on the
 * screen: when the platform refuses the full-screen intent the ring still happens, as a high-priority
 * notification with Stop and Schlummern on it. Which of the two this phone gets is recorded in
 * [fullScreenAllowed] and reported with the heartbeat, so the console can say so.
 *
 * It stops itself after [MAX_RING_MILLIS]: an alarm nobody hears must not ring all morning in a bag.
 */
class AlarmRingService : Service() {

    private val handler = Handler(Looper.getMainLooper())
    private var player: MediaPlayer? = null
    private var restoreVolume: Int? = null
    private var ringing = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> finish(snooze = false)
            ACTION_SNOOZE -> finish(snooze = true)
            else -> ring()
        }
        return START_NOT_STICKY
    }

    private fun ring() {
        val notification = notification()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
        if (ringing) return
        ringing = true
        raiseVolume()
        playTone()
        vibrate()
        handler.postDelayed({ finish(snooze = false) }, MAX_RING_MILLIS)
        Log.i(TAG, "ringing (full screen allowed: ${fullScreenAllowed(this)})")
    }

    private fun raiseVolume() {
        val audio = getSystemService(AudioManager::class.java) ?: return
        val current = runCatching { audio.getStreamVolume(AudioManager.STREAM_ALARM) }.getOrNull()
        val level = AlarmVolume.ringLevel(current, audio.getStreamMaxVolume(AudioManager.STREAM_ALARM)) ?: return
        // Restored only when it was read: an unread level restored as a guess would be wrong
        // tomorrow morning too (the Siren's rule, FR-9).
        restoreVolume = current
        runCatching { audio.setStreamVolume(AudioManager.STREAM_ALARM, level, 0) }
    }

    private fun playTone() {
        val uri = RingtoneManager.getActualDefaultRingtoneUri(this, RingtoneManager.TYPE_ALARM)
            ?: RingtoneManager.getDefaultUri(RingtoneManager.TYPE_ALARM)
            ?: RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE)
        player = runCatching {
            MediaPlayer().apply {
                setAudioAttributes(
                    AudioAttributes.Builder()
                        .setUsage(AudioAttributes.USAGE_ALARM)
                        .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                        .build()
                )
                setDataSource(this@AlarmRingService, uri)
                isLooping = true
                prepare()
                start()
            }
        }.onFailure { Log.w(TAG, "the alarm tone could not play: $it") }.getOrNull()
    }

    /* VibratorManager is API 31 and the floor is 29: on Android 10 and 11 the class does not exist,
       and naming it throws NoClassDefFoundError — inside the alarm, while it rings. Android lint's
       NewApi reported both call sites; CI did not run lint, so nothing was red. */
    private fun vibrator(): Vibrator? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            getSystemService(VibratorManager::class.java)?.defaultVibrator
        } else {
            @Suppress("DEPRECATION") // the VibratorManager route is API 31; minSdk here is 29
            getSystemService(Vibrator::class.java)
        }

    private fun vibrate() {
        val vibrator = vibrator() ?: return
        runCatching { vibrator.vibrate(VibrationEffect.createWaveform(longArrayOf(0, 800, 600), 0)) }
    }

    private fun finish(snooze: Boolean) {
        handler.removeCallbacksAndMessages(null)
        player?.let { runCatching { it.stop(); it.release() } }
        player = null
        vibrator()?.cancel()
        restoreVolume?.let { level ->
            runCatching { getSystemService(AudioManager::class.java)?.setStreamVolume(AudioManager.STREAM_ALARM, level, 0) }
        }
        restoreVolume = null
        ringing = false
        // On the main thread on purpose, unlike the receivers (AlarmReceivers.offMainThread): this
        // is a tap on an alarm that is already ringing, so the encrypted store was opened minutes
        // ago and the write is short — and stopSelf follows at once, so a write handed to a thread
        // could be lost with the process, and a lost snooze is an alarm that never rings again.
        if (snooze) {
            AlarmClock.snooze(this, Instant.now().plusMillis(SNOOZE_MILLIS))
        } else {
            AlarmClock.stopped(this)
        }
        sendBroadcast(Intent(ACTION_FINISHED).setPackage(packageName))
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
        Log.i(TAG, if (snooze) "snoozed for 5 min" else "stopped")
    }

    override fun onDestroy() {
        handler.removeCallbacksAndMessages(null)
        player?.let { runCatching { it.release() } }
        super.onDestroy()
    }

    private fun notification(): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        manager?.createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.alarm_channel), NotificationManager.IMPORTANCE_HIGH).apply {
                // The service plays the tone itself, on the alarm stream; a channel sound would play
                // on the notification stream on top of it.
                setSound(null, null)
                enableVibration(false)
            }
        )
        val screen = PendingIntent.getActivity(
            this, 0, Intent(this, AlarmActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setContentTitle(getString(R.string.alarm_title))
            .setContentText(getString(R.string.alarm_text))
            .setSmallIcon(android.R.drawable.ic_lock_idle_alarm)
            .setCategory(NotificationCompat.CATEGORY_ALARM)
            .setPriority(NotificationCompat.PRIORITY_MAX)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setOngoing(true)
            .setContentIntent(screen)
            .setFullScreenIntent(screen, true)
            .addAction(0, getString(R.string.alarm_snooze), action(ACTION_SNOOZE, 1))
            .addAction(0, getString(R.string.alarm_stop), action(ACTION_STOP, 2))
            .build()
    }

    private fun action(name: String, code: Int): PendingIntent = PendingIntent.getService(
        this, code, Intent(this, AlarmRingService::class.java).setAction(name),
        PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
    )

    companion object {
        private const val TAG = "FamilyGuard/Alarm"
        private const val CHANNEL = "alarm"
        private const val NOTIFICATION_ID = 7310
        const val ACTION_STOP = "io.github.helios57.familyguard.ALARM_STOP"
        const val ACTION_SNOOZE = "io.github.helios57.familyguard.ALARM_SNOOZE"
        const val ACTION_FINISHED = "io.github.helios57.familyguard.ALARM_FINISHED"
        const val SNOOZE_MILLIS = 5 * 60_000L
        const val MAX_RING_MILLIS = 10 * 60_000L

        fun start(context: Context) {
            context.startForegroundService(Intent(context, AlarmRingService::class.java))
        }

        fun send(context: Context, action: String) {
            context.startService(Intent(context, AlarmRingService::class.java).setAction(action))
        }

        /**
         * Whether the platform lets this app take over the screen for the alarm. Null below
         * Android 14, where the question did not exist and the permission is granted at install.
         */
        fun fullScreenAllowed(context: Context): Boolean? {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return null
            return context.getSystemService(NotificationManager::class.java)?.canUseFullScreenIntent()
        }
    }
}
