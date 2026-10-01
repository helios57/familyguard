package io.github.helios57.familyguard.plan

import android.content.Context
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.net.ApiClient

/**
 * Sends the child's "Mehr Zeit erbitten" (FR-28) and keeps the answer for the Heute screen.
 *
 * Its own class for the reason [TaskReporter] is: the screen is readable by anyone holding the phone
 * and never touches the device's credential.
 */
object TimeRequester {

    /** @throws IllegalStateException when the phone is not enrolled; ApiException / IOException as sent. */
    fun request(context: Context, minutes: Int, note: String): DayPlan {
        val store = EncryptedCredentialStore(context)
        val credentials = store.load() ?: error("this phone is not enrolled")
        val plan = ApiClient(credentials.serverUrl, token = { store.load()?.deviceToken }).requestTime(minutes, note)
        EncryptedDayPlanStore(context).save(plan)
        return plan
    }
}
