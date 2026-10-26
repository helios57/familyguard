package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState

enum class Verdict { ALLOW, REJECT }

/**
 * Whether an incoming call may ring (FR-31.2, FR-31.3). Pure: the screening service supplies the
 * state, the call and the platform's number normaliser.
 *
 * The platform passes the call-screening app only calls from numbers that are NOT the phone's own
 * contacts, so a call that reaches this is a stranger unless it is one of the family numbers. The
 * two mistakes cost very differently — a wrong ALLOW lets one stranger ring, a wrong REJECT silently
 * cuts a child off from a parent — so every case where the phone cannot decide is an ALLOW: no
 * policy yet, no number, a number nothing can read, a normaliser that throws. Outgoing calls, which
 * the platform also passes from API 30, are never this function's business.
 */
object CallDecision {

    fun decide(state: DesiredState?, incoming: Boolean, number: String?, normalise: (String) -> String?): Verdict {
        if (state == null || !state.callsContactsOnly || !incoming) return Verdict.ALLOW
        val raw = number?.trim().orEmpty()
        if (raw.isEmpty()) return Verdict.ALLOW
        val e164 = runCatching { normalise(raw) }.getOrElse { return Verdict.ALLOW }
        // Unreadable by the platform is compared as it arrived, digits only: a family number the SIM's
        // country rules cannot parse must still match itself.
        val candidates = setOfNotNull(e164, raw.filter { it.isDigit() || it == '+' }.ifEmpty { null })
        return if (candidates.any { it in state.familyNumbers }) Verdict.ALLOW else Verdict.REJECT
    }
}
