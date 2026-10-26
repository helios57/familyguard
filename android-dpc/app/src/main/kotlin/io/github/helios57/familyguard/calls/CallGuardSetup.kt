package io.github.helios57.familyguard.calls

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.role.RoleManager
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import io.github.helios57.familyguard.R

/**
 * FR-31.4 on the phone: whether FamilyGuard holds the call-screening role, and the prompt that gets
 * it. The role cannot be granted by a device owner — Android asks a person, once, in its own dialog —
 * so while the parent's switch is on and the role is not held the phone shows "Anrufschutz
 * einrichten", and tapping it opens that dialog ([CallGuardSetupActivity]).
 */
object CallGuardSetup {

    private const val CHANNEL = "family-guard-setup"
    private const val ID = 4131

    fun roleHeld(context: Context): Boolean = runCatching {
        val roles = context.getSystemService(RoleManager::class.java) ?: return false
        roles.isRoleAvailable(RoleManager.ROLE_CALL_SCREENING) && roles.isRoleHeld(RoleManager.ROLE_CALL_SCREENING)
    }.getOrDefault(false)

    /** Shows the prompt when [show], withdraws it otherwise. Cheap to call on every sync. */
    fun prompt(context: Context, show: Boolean) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (!show) {
            manager.cancel(ID)
            return
        }
        // The setup channel the usage-access notice already uses: both are things a person must do
        // once on this phone, and a second channel would be a second switch to find.
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.setup_channel), NotificationManager.IMPORTANCE_DEFAULT),
        )
        val open = PendingIntent.getActivity(
            context, 6, Intent(context, CallGuardSetupActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val text = context.getString(R.string.call_guard_setup_text)
        val notification = NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_familyguard)
            .setContentTitle(context.getString(R.string.call_guard_setup_title))
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        runCatching { manager.notify(ID, notification) }
    }
}
