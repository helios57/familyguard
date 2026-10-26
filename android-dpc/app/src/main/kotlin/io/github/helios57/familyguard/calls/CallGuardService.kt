package io.github.helios57.familyguard.calls

import android.content.Context
import android.telecom.Call
import android.telecom.CallScreeningService
import android.telephony.PhoneNumberUtils
import android.telephony.TelephonyManager
import android.util.Log
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.recovery.AndroidRecoveryStore
import io.github.helios57.familyguard.recovery.RecoveryMode
import io.github.helios57.familyguard.sync.ConnectionService
import io.github.helios57.familyguard.sync.EncryptedPolicyCache
import java.util.Locale
import java.util.UUID
import java.util.concurrent.Executors

/**
 * FR-31.2: the platform asks this service about every call from a number that is not one of the
 * phone's contacts, once FamilyGuard holds the call-screening role (FR-31.4). It never sees the
 * contacts — READ_CONTACTS is not requested, and the platform does the matching — and it never sees
 * calls from a hidden number, which do not reach a screening app at all (FR-31.6).
 *
 * The answer comes off the main thread, because the cached policy lives in encrypted preferences and
 * the first read opens the keystore. Telecom waits about five seconds and then lets the call ring on
 * its own; every failure here answers ALLOW sooner than that, for the reason [CallDecision] gives.
 */
class CallGuardService : CallScreeningService() {

    private val worker = Executors.newSingleThreadExecutor()

    override fun onScreenCall(details: Call.Details) {
        worker.execute {
            CallScreening(
                policy = { EncryptedPolicyCache(this).load()?.let { EnforcementEngine.compute(it) } },
                released = { RecoveryMode(AndroidRecoveryStore(this).mode).active() },
                normalise = { PhoneNumberUtils.formatNumberToE164(it, simCountry(this)) },
                record = { number ->
                    val shown = runCatching { PhoneNumberUtils.formatNumberToE164(number, simCountry(this)) }.getOrNull() ?: number
                    rejectedCalls(this).add(shown, System.currentTimeMillis())
                    // A sync now, so the parent sees the call in seconds rather than at the next interval. A
                    // device owner may start its foreground service from here; if the platform refuses, the
                    // call waits in the queue for the next sync, which is all it would have done anyway.
                    runCatching { ConnectionService.wake(this, ConnectionService.ACTION_CALL_GUARD) }
                },
                warn = { message, e -> Log.w(TAG, message, e) },
                isEmergency = { getSystemService(TelephonyManager::class.java)?.isEmergencyNumber(it) == true },
                emergency = PreferencesEmergencyMark(this),
            ).screen(
                incoming = details.callDirection == Call.Details.DIRECTION_INCOMING,
                number = details.handle?.schemeSpecificPart,
            ) { respond(details, it) }
        }
    }

    private fun respond(details: Call.Details, verdict: Verdict) {
        val response = if (verdict == Verdict.REJECT) {
            // No ring and no missed-call notification. Android lists the call in the call log as
            // blocked whatever setSkipCallLog says (measured, FR-31.2); the console is where the parent
            // sees it (FR-31.5).
            CallResponse.Builder()
                .setDisallowCall(true)
                .setRejectCall(true)
                .setSkipCallLog(true)
                .setSkipNotification(true)
                .build()
        } else {
            CallResponse.Builder().build()
        }
        runCatching { respondToCall(details, response) }.onFailure { Log.w(TAG, "could not answer Telecom", it) }
    }

    override fun onDestroy() {
        worker.shutdown()
        super.onDestroy()
    }

    companion object {
        private const val TAG = "CallGuard"

        /** The SIM's country for reading a national number, the network's when there is no SIM, CH last. */
        fun simCountry(context: Context): String {
            val tm = context.getSystemService(TelephonyManager::class.java)
            val iso = tm?.simCountryIso?.takeIf { it.isNotBlank() } ?: tm?.networkCountryIso?.takeIf { it.isNotBlank() }
            return (iso ?: "ch").uppercase(Locale.ROOT)
        }

        /** Plain preferences: an instant is not sensitive, and this must be readable within Telecom's five seconds. */
        private class PreferencesEmergencyMark(context: Context) : EmergencyMark {
            private val prefs = context.applicationContext.getSharedPreferences("call_guard", Context.MODE_PRIVATE)
            override fun last(): Long? = prefs.getLong(KEY_EMERGENCY_AT, -1L).takeIf { it >= 0 }
            override fun set(atMillis: Long) {
                prefs.edit().putLong(KEY_EMERGENCY_AT, atMillis).commit()
            }
        }

        private const val KEY_EMERGENCY_AT = "emergency_dialled_at"

        fun rejectedCalls(context: Context) = RejectedCallLog(EncryptedRejectedCallStore(context.applicationContext)) {
            UUID.randomUUID().toString()
        }
    }
}
