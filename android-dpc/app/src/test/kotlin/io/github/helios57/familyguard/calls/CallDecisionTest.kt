package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-31.2 and FR-31.3: which calls the phone lets ring. The platform hands the screening service only
 * calls from numbers that are not contacts, so every call that reaches [CallDecision] is a stranger
 * unless it is a family number. The asymmetry is the point of every test here: a wrong ALLOW lets a
 * stranger ring once, a wrong REJECT silently cuts a child off from a parent — so whenever the phone
 * cannot decide, it allows.
 */
class CallDecisionTest {

    private val on = DesiredState(callsContactsOnly = true, familyNumbers = listOf("+41790000001", "+15550101"))

    /** A stand-in for PhoneNumberUtils with the SIM in Switzerland. */
    private val swiss: (String) -> String? = { raw ->
        val digits = raw.filter { it.isDigit() || it == '+' }
        when {
            digits.startsWith("+") -> digits
            digits.startsWith("00") -> "+" + digits.drop(2)
            digits.startsWith("0") -> "+41" + digits.drop(1)
            else -> null
        }
    }

    @Test
    fun `a stranger is rejected while the switch is on`() {
        assertEquals(Verdict.REJECT, CallDecision.decide(on, incoming = true, number = "+15550199", normalise = swiss))
    }

    @Test
    fun `the switch off lets a stranger ring`() {
        assertEquals(Verdict.ALLOW, CallDecision.decide(on.copy(callsContactsOnly = false), true, "+15550199", swiss))
    }

    @Test
    fun noStateAllowsEverything() {
        assertEquals(Verdict.ALLOW, CallDecision.decide(null, true, "+15550199", swiss))
    }

    @Test
    fun outgoingIsAlwaysAllowed() {
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, incoming = false, number = "+15550199", normalise = swiss))
    }

    @Test
    fun familyNumberInNationalFormPasses() {
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "079 000 00 01", swiss))
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "0041790000001", swiss))
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "+1 555 0101", swiss))
    }

    @Test
    fun `a number that is neither given nor readable is let through`() {
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, null, swiss))
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "", swiss))
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "   ", swiss))
    }

    @Test
    fun `a number the normaliser cannot read is compared as it arrived`() {
        val noRead: (String) -> String? = { null }
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "+41790000001", noRead))
        assertEquals(Verdict.REJECT, CallDecision.decide(on, true, "+15550199", noRead))
    }

    @Test
    fun `a normaliser that throws lets the call through`() {
        val broken: (String) -> String? = { error("no telephony") }
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "+15550199", broken))
    }

    @Test
    fun `a national number the platform cannot read still matches its family number`() {
        val unreadable: (String) -> String? = { null }
        assertEquals(Verdict.ALLOW, CallDecision.decide(on, true, "079 000 00 01", unreadable))
        assertEquals(Verdict.REJECT, CallDecision.decide(on, true, "079 000 00 09", unreadable))
        // Too short to say whose it is: four digits must not match the end of every family number.
        assertEquals(Verdict.REJECT, CallDecision.decide(on, true, "0001", unreadable))
    }
}
