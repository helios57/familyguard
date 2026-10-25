package io.github.helios57.familyguard.commands

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.view.KeyEvent
import android.view.WindowManager
import android.window.OnBackInvokedCallback
import android.window.OnBackInvokedDispatcher
import com.google.android.material.button.MaterialButton
import io.github.helios57.familyguard.R

/**
 * The siren's Stopp screen (FR-9.4): over the lock screen, display on, one big button — and any key
 * at all stops it, because a person who has just found a ringing phone presses whatever is under
 * their thumb.
 *
 * It holds the display on while it is visible, which is what lets [AndroidSirenDevice] read the
 * display turning off as the side key: nothing else can turn it off then. [hold] is how it says so.
 *
 * It reads no input from its Intent. What a press does is handed over in [pressed] by the
 * [AndroidSirenDevice] that started it, in the same process — the stop itself belongs to the
 * service that holds the playing tone, and this screen only reports that somebody pressed.
 */
class SirenActivity : Activity() {

    private val finished = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) = finish()
    }

    private var backCallback: OnBackInvokedCallback? = null

    /** The siren this screen was shown for: a screen still closing must not speak for the next one. */
    private var screenHold = ScreenHold()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setShowWhenLocked(true)
        setTurnScreenOn(true)
        screenHold = hold
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        setContentView(R.layout.activity_siren)
        findViewById<MaterialButton>(R.id.siren_stop).setOnClickListener { press() }
        // Anywhere on the screen, not only the button: a hand grabbing a phone lands where it lands.
        findViewById<android.view.View>(R.id.siren_root).setOnClickListener { press() }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            // From API 33 with predictive back, Back never arrives as a key event; it arrives here.
            val callback = OnBackInvokedCallback { press() }
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT, callback)
            backCallback = callback
        }
        registerReceiver(finished, IntentFilter(ACTION_FINISHED), RECEIVER_NOT_EXPORTED)
    }

    override fun onStart() {
        super.onStart()
        screenHold.onStart()
        shown?.invoke()
    }

    /** Covered or gone: the flag holds the display no longer, a moment from now (see [ScreenHold]). */
    override fun onStop() {
        screenHold.onStop(SystemClock.elapsedRealtime())
        super.onStop()
    }

    /** Every key, before any view can consume it — volume up and down above all (FR-9.4). */
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (event.action == KeyEvent.ACTION_DOWN) press()
        return true
    }

    /** Home and Recents are buttons too, and they do not arrive as keys. */
    override fun onUserLeaveHint() {
        super.onUserLeaveHint()
        press()
    }

    private fun press() {
        pressed?.invoke(SystemClock.elapsedRealtime())
        finish()
    }

    override fun onDestroy() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            backCallback?.let { onBackInvokedDispatcher.unregisterOnBackInvokedCallback(it) }
        }
        unregisterReceiver(finished)
        super.onDestroy()
    }

    companion object {
        /** Sent, to this package only, when the siren stops for any reason; the screen closes. */
        const val ACTION_FINISHED = "io.github.helios57.familyguard.SIREN_FINISHED"

        /**
         * Whether this screen is holding the display on. Not cleared the instant it stops, because the
         * side key stops it a moment before the display-off broadcast arrives, and that broadcast is
         * the press; but covered for longer — by a call's screen, say — it holds nothing, and a
         * display timing out then is not a press.
         */
        @Volatile
        var hold = ScreenHold()

        /** Called each time this screen comes up; [AndroidSirenDevice] drops the full-screen intent. */
        @Volatile
        var shown: (() -> Unit)? = null

        /** What a press does, set by the [AndroidSirenDevice] that showed this screen; elapsed-realtime ms. */
        @Volatile
        var pressed: ((Long) -> Unit)? = null
    }
}
