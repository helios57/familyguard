package io.github.helios57.familyguard.alarm

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import java.time.Instant

/** The platform's call at the booked minute: ring, and book the next one (FR-23.4). */
class AlarmFireReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != AlarmClock.ACTION_FIRE) return
        val at = intent.getLongExtra(AlarmClock.EXTRA_AT, 0L).takeIf { it > 0 }?.let(Instant::ofEpochMilli)
            ?: Instant.now()
        AlarmRingService.start(context)
        AlarmClock.fired(context, at)
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
            Intent.ACTION_TIME_CHANGED, Intent.ACTION_TIMEZONE_CHANGED -> AlarmClock.rebook(context)
        }
    }
}
