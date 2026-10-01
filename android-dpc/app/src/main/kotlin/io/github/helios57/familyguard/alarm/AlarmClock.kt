package io.github.helios57.familyguard.alarm

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.util.Log
import io.github.helios57.familyguard.recovery.RecoveryActivity
import java.time.Instant

/**
 * Books the next ring with the platform (FR-23.3).
 *
 * `setAlarmClock` rather than the exact alarms the rest of this app books: it is the one alarm the
 * platform promises to deliver on time in Doze — the others can be deferred, which this project has
 * measured (the update check drifting 6–21 minutes on a restricted phone) — and it is the one that
 * puts the next alarm in the status bar and on the lock screen, where a child looks for it.
 *
 * Re-booked from everything that can move the answer: a new rule from the server, a ring, a snooze,
 * a stop, a boot, an update of this app, a change of time or timezone. Each call replaces the booking
 * (one PendingIntent, FLAG_UPDATE_CURRENT), so calling it too often costs nothing.
 *
 * Every entry point is `@Synchronized`, because each is read-decide-book and its callers are on
 * different threads: the receivers' background threads, the sync's IO thread, the ring service's
 * main thread. Two interleaved can land out of order — an `update` that read the state a moment
 * before `fired` recorded the ring books that minute again after `fired` booked tomorrow, and the
 * alarm rings twice. The monitor is reentrant, so the entry points that end in [rebook] are fine.
 */
object AlarmClock {

    private const val TAG = "FamilyGuard/Alarm"
    const val ACTION_FIRE = "io.github.helios57.familyguard.ALARM_FIRE"
    const val EXTRA_AT = "at"
    private const val REQUEST_FIRE = 7301
    private const val REQUEST_SHOW = 7302

    /** A new rule from the server. */
    @Synchronized
    fun update(context: Context, schedule: AlarmSchedule) {
        val store = EncryptedAlarmStore(context)
        if (store.schedule() == schedule) return
        store.saveSchedule(schedule)
        rebook(context)
    }

    /** Books the next ring, or cancels the booking when there is none. Returns what was booked. */
    @Synchronized
    fun rebook(context: Context, now: Instant = Instant.now()): Instant? {
        val store = EncryptedAlarmStore(context)
        val schedule = store.schedule() ?: AlarmSchedule()
        val at = AlarmBooking.next(schedule, now, store.state())
        val manager = context.getSystemService(AlarmManager::class.java) ?: return null
        val fire = firePendingIntent(context, at)
        if (at == null) {
            manager.cancel(fire)
            Log.i(TAG, "no alarm to book")
            return null
        }
        val show = PendingIntent.getActivity(
            context, REQUEST_SHOW, Intent(context, RecoveryActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val booked = runCatching {
            manager.setAlarmClock(AlarmManager.AlarmClockInfo(at.toEpochMilli(), show), fire)
        }
        if (booked.isFailure) {
            // Only without the exact-alarm permission, which USE_EXACT_ALARM grants at install on
            // Android 13+. A wake-up the platform may delay beats none.
            Log.w(TAG, "setAlarmClock refused (${booked.exceptionOrNull()}); booking an inexact wake-up")
            manager.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at.toEpochMilli(), fire)
        } else {
            Log.i(TAG, "alarm booked for $at")
        }
        return at
    }

    /** The booked ring has come: remember it so it is not booked again, then book the next. */
    @Synchronized
    fun fired(context: Context, at: Instant) {
        val store = EncryptedAlarmStore(context)
        store.saveState(AlarmState(snoozeUntil = null, lastRang = at))
        rebook(context)
    }

    @Synchronized
    fun snooze(context: Context, until: Instant) {
        val store = EncryptedAlarmStore(context)
        store.saveState(store.state().copy(snoozeUntil = until))
        rebook(context)
    }

    @Synchronized
    fun stopped(context: Context) {
        val store = EncryptedAlarmStore(context)
        store.saveState(store.state().copy(snoozeUntil = null))
        rebook(context)
    }

    private fun firePendingIntent(context: Context, at: Instant?): PendingIntent {
        val intent = Intent(context, AlarmFireReceiver::class.java).setAction(ACTION_FIRE)
        if (at != null) intent.putExtra(EXTRA_AT, at.toEpochMilli())
        return PendingIntent.getBroadcast(
            context, REQUEST_FIRE, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}
