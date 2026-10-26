package io.github.helios57.familyguard.calls

import android.content.Context
import android.telecom.Call
import android.telecom.CallScreeningService
import android.telephony.PhoneNumberUtils
import android.telephony.TelephonyManager
import android.util.Log
import io.github.helios57.familyguard.enforce.EnforcementEngine
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
            val verdict = runCatching { screen(details) }
                .onFailure { Log.w(TAG, "screening failed, letting the call through", it) }
                .getOrDefault(Verdict.ALLOW)
            respond(details, verdict)
        }
    }

    private fun screen(details: Call.Details): Verdict {
        val state = EncryptedPolicyCache(this).load()?.let { EnforcementEngine.compute(it) }
        val number = details.handle?.schemeSpecificPart
        val incoming = details.callDirection == Call.Details.DIRECTION_INCOMING
        val country = simCountry(this)
        val verdict = CallDecision.decide(state, incoming, number) { PhoneNumberUtils.formatNumberToE164(it, country) }
        if (verdict == Verdict.REJECT && number != null) {
            val shown = runCatching { PhoneNumberUtils.formatNumberToE164(number, country) }.getOrNull() ?: number
            rejectedCalls(this).add(shown, System.currentTimeMillis())
        }
        return verdict
    }

    private fun respond(details: Call.Details, verdict: Verdict) {
        val response = if (verdict == Verdict.REJECT) {
            // No ring, no entry in the call log, no missed-call notification: a stranger's call simply
            // did not happen on this phone. The console is where it is visible (FR-31.5).
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

        fun rejectedCalls(context: Context) = RejectedCallLog(EncryptedRejectedCallStore(context.applicationContext)) {
            UUID.randomUUID().toString()
        }
    }
}
