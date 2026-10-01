package io.github.helios57.familyguard.alarm

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import java.time.Instant
import kotlin.concurrent.thread

/** The platform's call at the booked minute: ring, and book the next one (FR-23.4). */
class AlarmFireReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != AlarmClock.ACTION_FIRE) return
        val at = intent.getLongExtra(AlarmClock.EXTRA_AT, 0L).takeIf { it > 0 }?.let(Instant::ofEpochMilli)
            ?: Instant.now()
        // The ring first, and here: it is one binder call, and it is the thing the child must
        // hear. Remembering the ring and booking the next one is a keystore read, a commit and an
        // AlarmManager call, and none of it may stand between the minute and the sound.
        AlarmRingService.start(context)
        val app = context.applicationContext
        offMainThread("fg-alarm-fired") { AlarmClock.fired(app, at) }
    }
}

/**
 * A change of the phone's clock or timezone moves every booked instant, so the next ring is computed
 * again. The rule is read in the profile's timezone, so a phone that travels still rings at the
 * family's 06:30 — the same zone bedtime and the daily limit are read in.
 */
class TimeChangeReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_TIME_CHANGED, Intent.ACTION_TIMEZONE_CHANGED -> {
                val app = context.applicationContext
                offMainThread("fg-alarm-rebook") { AlarmClock.rebook(app) }
            }
        }
    }
}

/**
 * Runs [work] on its own thread and keeps the broadcast open until it has finished.
 *
 * `onReceive` is on the main thread, and [AlarmClock] opens EncryptedSharedPreferences (a keystore
 * round trip and a synchronous commit) before it reaches AlarmManager. `goAsync` is what keeps the
 * process alive for the write once `onReceive` has returned — without it the platform may kill a
 * cached process between the two halves, and the booking is lost with nothing logged. `finish` is
 * in a `finally` so a failure cannot hold the broadcast open until the platform gives up on it.
 *
 * Concurrency with the other callers of [AlarmClock] (a sync, the ring service, boot) is handled
 * there, not here: its writes are serialised on the object.
 */
private fun BroadcastReceiver.offMainThread(name: String, work: () -> Unit) {
    val pending = goAsync()
    thread(name = name) {
        try {
            work()
        } finally {
            pending.finish()
        }
    }
}
