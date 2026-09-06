package io.github.helios57.familyguard.sync

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import io.github.helios57.familyguard.enforce.AlarmBooking
import io.github.helios57.familyguard.enforce.AlarmPlatform

/**
 * Books a wake-up with `AlarmManager`, and reports honestly when it cannot.
 *
 * The alarm restarts [ConnectionService] rather than reaching a receiver of its own: the service is
 * where the synchronizer, the policy cache and the sync lock already live, and it is what has to be
 * running for the work to happen anyway. `getForegroundService` is the pending-intent kind that
 * lets a service started this way call `startForeground` from the background, which is exactly what
 * `onStartCommand` does before anything else.
 *
 * **Two callers, and the wake-up is the whole point of both.** [enforcement] starts a bedtime on a
 * phone lying face down; [updateCheck] is what makes FR-15.6 a cadence rather than a hope, because
 * the coroutine `delay` it replaces is measured on a clock that stops when the phone sleeps — see
 * [io.github.helios57.familyguard.update.UpdateSchedule]. `RTC_WAKEUP` plus `AllowWhileIdle` is the
 * only combination that both counts wall-clock time and is delivered to a dozing device.
 *
 * **Exactness is checked, never assumed.** `SCHEDULE_EXACT_ALARM` is an appop, and an app targeting
 * Android 13 or later does not hold it by default. Device-owner apps are documented as exempt on
 * some versions, but "documented as exempt" is not something this code can assert about the phone it
 * is running on — so it asks, and falls back to a wake-up the platform may delay rather than to no
 * wake-up at all. The fallback is logged as itself by whoever reads [AlarmBooking].
 */
class AlarmManagerPlatform(
    private val context: Context,
    private val action: String,
    private val requestCode: Int,
) : AlarmPlatform {

    private var reason: String = "nothing has been booked yet"

    override fun schedule(atMillis: Long): AlarmBooking {
        val manager = context.getSystemService(AlarmManager::class.java)
        if (manager == null) {
            reason = "this device has no AlarmManager"
            return AlarmBooking.REFUSED
        }
        val intent = pendingIntent(PendingIntent.FLAG_UPDATE_CURRENT)
        if (intent == null) {
            reason = "the wake-up PendingIntent could not be created"
            return AlarmBooking.REFUSED
        }

        val exactAllowed = Build.VERSION.SDK_INT < Build.VERSION_CODES.S ||
            manager.canScheduleExactAlarms()
        if (exactAllowed) {
            // A SecurityException is still possible: the appop can be revoked between the check and
            // the call. Catching it turns a killed service into a delayed bedtime.
            val booked = runCatching {
                manager.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, atMillis, intent)
            }
            if (booked.isSuccess) {
                reason = ""
                return AlarmBooking.EXACT
            }
            reason = "the platform refused an exact alarm: ${booked.exceptionOrNull()}"
        } else {
            reason = "SCHEDULE_EXACT_ALARM is not granted to this app"
        }

        // `setAndAllowWhileIdle` still pierces doze; what it gives up is the minute it fires in.
        val inexact = runCatching {
            manager.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, atMillis, intent)
        }
        if (inexact.isFailure) {
            reason = "$reason, and an inexact one too: ${inexact.exceptionOrNull()}"
            return AlarmBooking.REFUSED
        }
        return AlarmBooking.INEXACT
    }

    override fun cancel() {
        val manager = context.getSystemService(AlarmManager::class.java) ?: return
        pendingIntent(PendingIntent.FLAG_NO_CREATE)?.let { manager.cancel(it) }
        reason = "cancelled"
    }

    override fun unavailableReason(): String = reason

    /**
     * @param flags `FLAG_UPDATE_CURRENT` when booking, `FLAG_NO_CREATE` when cancelling — creating
     * one only to cancel it would leave the system holding a PendingIntent this app never used.
     */
    private fun pendingIntent(flags: Int): PendingIntent? {
        val intent = Intent(context, ConnectionService::class.java).setAction(action)
        return PendingIntent.getForegroundService(
            context,
            requestCode,
            intent,
            // IMMUTABLE because nothing outside this app should be able to fill anything in, and
            // required from Android 12 regardless.
            PendingIntent.FLAG_IMMUTABLE or flags,
        )
    }

    companion object {
        /**
         * The request codes, allocated here and nowhere else.
         *
         * Stable, so every booking replaces the previous one instead of stacking — and distinct,
         * because two alarms sharing one code are one alarm: the second booking would silently
         * cancel the first, and the symptom would be whichever feature is booked less often simply
         * not happening.
         */
        private const val REQUEST_ENFORCE = 1
        private const val REQUEST_UPDATE_CHECK = 2

        /** The bedtime edge and the quota midnight (FR-4.2, NFR-10). */
        fun enforcement(context: Context): AlarmManagerPlatform =
            AlarmManagerPlatform(context, ConnectionService.ACTION_ENFORCE, REQUEST_ENFORCE)

        /** The automatic self-update check (FR-15.6). */
        fun updateCheck(context: Context): AlarmManagerPlatform =
            AlarmManagerPlatform(context, ConnectionService.ACTION_UPDATE_CHECK, REQUEST_UPDATE_CHECK)
    }
}
