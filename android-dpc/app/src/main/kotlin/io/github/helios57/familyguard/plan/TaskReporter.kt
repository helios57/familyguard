package io.github.helios57.familyguard.plan

import android.content.Context
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.net.ApiClient

/**
 * Sends the child's "Fertig" (FR-22) and keeps the answer for the Heute screen.
 *
 * Its own class rather than code in the screen, because the screen is readable by anyone holding the
 * phone and must never touch the device's credential (ManifestAndPlatformCallsTest holds that): the
 * token stays in here, and the screen only learns the day as it now stands, or that it failed.
 */
object TaskReporter {

    /** @throws IllegalStateException when the phone is not enrolled; ApiException / IOException as sent. */
    fun report(context: Context, taskId: String): DayPlan {
        val store = EncryptedCredentialStore(context)
        val credentials = store.load() ?: error("this phone is not enrolled")
        val plan = ApiClient(credentials.serverUrl, token = { store.load()?.deviceToken }).reportTask(taskId)
        EncryptedDayPlanStore(context).save(plan)
        return plan
    }
}
