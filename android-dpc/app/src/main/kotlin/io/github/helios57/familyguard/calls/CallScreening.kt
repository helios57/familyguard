package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.recovery.releasedState

/**
 * One call through the screening service, apart from Telecom so a JVM test can drive it: the verdict,
 * then the answer, then the bookkeeping — in that order. Telecom waits about five seconds and then
 * lets the call ring; writing the call down touches encrypted storage, which can be slow and is built
 * to throw when it cannot seal. Done first, either would turn a rejection into a ring.
 *
 * A phone released by a recovery code enforces nothing (FR-12.2), and that includes this: the person
 * calling a child whose phone was just recovered in an emergency may well be a stranger.
 */
class CallScreening(
    private val policy: () -> DesiredState?,
    private val released: () -> Boolean,
    private val normalise: (String) -> String?,
    private val record: (number: String) -> Unit,
    private val warn: (String, Throwable) -> Unit,
    private val isEmergency: (String) -> Boolean = { false },
    private val emergency: EmergencyMark = EmergencyMark.None,
    private val now: () -> Long = System::currentTimeMillis,
) {
    fun screen(incoming: Boolean, number: String?, respond: (Verdict) -> Unit) {
        val verdict = runCatching {
            if (incoming && afterEmergency()) {
                Verdict.ALLOW
            } else {
                CallDecision.decide(if (released()) releasedState() else policy(), incoming, number, normalise)
            }
        }.onFailure { warn("screening failed, letting the call through", it) }.getOrDefault(Verdict.ALLOW)
        respond(verdict)
        if (!incoming && number != null) {
            runCatching { if (isEmergency(number)) emergency.set(now()) }
                .onFailure { warn("could not tell whether an outgoing call was an emergency call", it) }
        }
        if (verdict == Verdict.REJECT && number != null) {
            runCatching { record(number) }.onFailure { warn("a rejected call was not written down", it) }
        }
    }

    private fun afterEmergency(): Boolean {
        val at = emergency.last() ?: return false
        return now() - at in 0 until EMERGENCY_WINDOW_MILLIS
    }

    companion object {
        /**
         * After the child dials an emergency number, every call rings for this long: the call-back from
         * the emergency centre comes from a number nobody has saved. Two hours is the platform's own
         * rule for its blocked numbers (`KEY_DURATION_BLOCKING_DISABLED_AFTER_EMERGENCY_INT`, carrier
         * default 7200 s); Telecom skips screening apps itself only in emergency call-back mode, which
         * not every network enters.
         */
        const val EMERGENCY_WINDOW_MILLIS = 2 * 60 * 60 * 1000L
    }
}

/** When the child last dialled an emergency number; kept across the service's process ending. */
interface EmergencyMark {
    fun last(): Long?
    fun set(atMillis: Long)

    object None : EmergencyMark {
        override fun last(): Long? = null
        override fun set(atMillis: Long) = Unit
    }
}
