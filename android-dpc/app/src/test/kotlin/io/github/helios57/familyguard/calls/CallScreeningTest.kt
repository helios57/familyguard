package io.github.helios57.familyguard.calls

import io.github.helios57.familyguard.enforce.DesiredState
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FR-31.2 around [CallDecision]: what the screening service does with one call. Telecom waits about
 * five seconds for the answer, and writing the call down touches encrypted storage, so the answer
 * goes first and nothing that happens after it can change it.
 */
class CallScreeningTest {

    private val on = DesiredState(callsContactsOnly = true, familyNumbers = listOf("+41790000001"))
    private val events = mutableListOf<String>()

    private var clock = 1_793_000_000_000L
    private val mark = object : EmergencyMark {
        var at: Long? = null
        override fun last() = at
        override fun set(atMillis: Long) { at = atMillis }
    }

    private fun screening(
        released: Boolean = false,
        record: (String) -> Unit = { events += "recorded $it" },
    ) = CallScreening(
        policy = { on },
        released = { released },
        normalise = { it },
        record = record,
        warn = { _, _ -> },
        isEmergency = { it == "112" },
        emergency = mark,
        now = { clock },
    )

    private fun CallScreening.call(number: String) = screen(incoming = true, number = number) { events += "answered $it" }

    @Test
    fun `the answer reaches Telecom before the call is written down`() {
        screening().call("+15550199")
        assertEquals(listOf("answered REJECT", "recorded +15550199"), events)
    }

    @Test
    fun `a call that cannot be written down is still rejected`() {
        screening(record = { error("keystore refused to seal") }).call("+15550199")
        assertEquals(listOf("answered REJECT"), events)
    }

    @Test
    fun `a phone released by a recovery code lets a stranger ring`() {
        // FR-12.2: released means nothing is enforced, and a rescuer calling from an unknown number
        // is exactly who must get through.
        screening(released = true).call("+15550199")
        assertEquals(listOf("answered ALLOW"), events)
    }

    @Test
    fun `a family number rings and is not written down`() {
        screening().call("+41790000001")
        assertEquals(listOf("answered ALLOW"), events)
    }

    @Test
    fun `after the child dials an emergency number every call rings for two hours`() {
        // The platform's own rule for its blocked numbers (carrier default 7200 s): the call-back from
        // the emergency centre comes from a number no child has saved.
        screening().screen(incoming = false, number = "112") { events += "answered $it" }
        clock += 2 * 60 * 60 * 1000L - 60_000L
        screening().call("+15550199")
        clock += 2 * 60_000L
        screening().call("+15550199")
        assertEquals(listOf("answered ALLOW", "answered ALLOW", "answered REJECT", "recorded +15550199"), events)
    }

    @Test
    fun `an ordinary outgoing call opens no window`() {
        screening().screen(incoming = false, number = "+15550111") { events += "answered $it" }
        screening().call("+15550199")
        assertEquals(listOf("answered ALLOW", "answered REJECT", "recorded +15550199"), events)
    }
}
