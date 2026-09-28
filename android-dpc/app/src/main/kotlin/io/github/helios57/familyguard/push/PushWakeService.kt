package io.github.helios57.familyguard.push

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import io.github.helios57.familyguard.sync.ConnectionService

/**
 * The push wake-up (FR-26.3). The message says nothing but "sync": the phone then asks the server what
 * changed, exactly as it would after a poll, so nothing about the child passes through Google.
 */
class PushWakeService : FirebaseMessagingService() {

    override fun onMessageReceived(message: RemoteMessage) {
        if (message.data["t"] != "sync") {
            Log.w(TAG, "push: ignored a message that is not a wake-up")
            return
        }
        PushRegistrar.received(this)
        ConnectionService.wake(this, ConnectionService.ACTION_PUSH)
    }

    /** FCM registered this installation, possibly under a new ID (FR-26.3). */
    override fun onRegistered(installationId: String) {
        if (PushRegistrar.tokenChanged(this, installationId)) ConnectionService.wake(this, ConnectionService.ACTION_PUSH_TOKEN)
    }

    /**
     * FCM dropped the registration. The phone polls every five minutes again, and the next sync starts
     * a new registration.
     */
    override fun onUnregistered(installationId: String) {
        PushRegistrar.unregistered(this)
    }

    private companion object {
        const val TAG = "FamilyGuard/Push"
    }
}
