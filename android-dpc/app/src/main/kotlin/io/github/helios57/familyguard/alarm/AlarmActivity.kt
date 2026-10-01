package io.github.helios57.familyguard.alarm

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Bundle
import android.widget.Button
import android.widget.TextView
import io.github.helios57.familyguard.R
import java.time.LocalTime
import java.time.format.DateTimeFormatter

/**
 * The alarm over the lock screen (FR-23.4): the time, *Stop* and *Schlummern*. It changes nothing
 * about the schedule — that belongs to a parent — and it reads no input from its Intent.
 */
class AlarmActivity : Activity() {

    private val finished = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) = finish()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setShowWhenLocked(true)
        setTurnScreenOn(true)
        setContentView(R.layout.activity_alarm)
        findViewById<TextView>(R.id.alarm_time).text = LocalTime.now().format(DateTimeFormatter.ofPattern("HH:mm"))
        // "Montag, 5. Oktober" in the phone's language, from the platform's own pattern for it.
        val locale = resources.configuration.locales[0]
        findViewById<TextView>(R.id.alarm_day).text = java.time.LocalDate.now().format(
            DateTimeFormatter.ofPattern(android.text.format.DateFormat.getBestDateTimePattern(locale, "EEEEdMMMM"), locale),
        )
        findViewById<Button>(R.id.alarm_stop).setOnClickListener {
            AlarmRingService.send(this, AlarmRingService.ACTION_STOP)
            finish()
        }
        findViewById<Button>(R.id.alarm_snooze).setOnClickListener {
            AlarmRingService.send(this, AlarmRingService.ACTION_SNOOZE)
            finish()
        }
        registerReceiver(finished, IntentFilter(AlarmRingService.ACTION_FINISHED), RECEIVER_NOT_EXPORTED)
    }

    override fun onDestroy() {
        unregisterReceiver(finished)
        super.onDestroy()
    }
}
