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
        val digits = raw.filter { it.isDigit() || it == '+' }
        val candidates = setOfNotNull(e164, digits.ifEmpty { null })
        if (candidates.any { it in state.familyNumbers }) return Verdict.ALLOW
        if (e164 == null && nationalMatch(digits, state.familyNumbers)) return Verdict.ALLOW
        return Verdict.REJECT
    }

    /**
     * A national number (`079…`) the platform could not turn into E.164 is the family number whose
     * E.164 form ends in the same digits after the 0, behind a one- to three-digit country code. That
     * can let a stranger in another country with the same national digits ring; it cannot cut a parent
     * off, which is the mistake that matters. Fewer than eight digits say too little to compare.
     */
    private fun nationalMatch(digits: String, family: List<String>): Boolean {
        if (!digits.startsWith("0") || digits.startsWith("00")) return false
        val national = digits.drop(1)
        if (national.length < 8) return false
        return family.any { it.startsWith("+") && it.endsWith(national) && it.length - national.length in 2..4 }
    }
}
