package io.github.helios57.familyguard.plan

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import io.github.helios57.familyguard.R
import io.github.helios57.familyguard.recovery.RecoveryActivity

/**
 * Tells the child a parent answered "Mehr Zeit erbitten" (FR-28.2), the moment the phone hears of
 * it. A child who asked is looking at a paused app, not at the FamilyGuard screen.
 *
 * Its own channel at IMPORTANCE_HIGH: an answer is something to see now, and a channel's importance
 * cannot be raised after it is created.
 */
object AnswerNotifier {

    private const val CHANNEL = "time-answers"
    private const val ID = 4128

    fun show(context: Context, answer: DayTimeRequest) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.answer_channel), NotificationManager.IMPORTANCE_HIGH),
        )
        val granted = answer.state == DayTimeRequest.GRANTED
        val title = if (granted) {
            context.getString(R.string.answer_granted_title, minutes(context, answer.grantedMinutes))
        } else {
            context.getString(R.string.answer_declined_title)
        }
        val text = context.getString(if (granted) R.string.answer_granted_text else R.string.answer_declined_text)
        val open = PendingIntent.getActivity(
            context, 5, Intent(context, RecoveryActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val notification = NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(android.R.drawable.ic_menu_recent_history)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .build()
        // A phone without the notification permission says nothing, and the Heute screen still does.
        runCatching { manager.notify(ID, notification) }
    }

    private fun minutes(context: Context, minutes: Int): String {
        val h = minutes / 60
        val m = minutes % 60
        return when {
            h == 0 -> context.getString(R.string.duration_minutes, m)
            m == 0 -> context.getString(R.string.duration_hours, h)
            else -> context.getString(R.string.duration_hours_minutes, h, m)
        }
    }
}
