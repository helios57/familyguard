package io.github.helios57.familyguard.push

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Where this phone registers for push (FR-26.3), as the server hands it out with the policy. None of
 * it is secret — an Android app's Firebase API key ships inside every app that uses one — and it comes
 * from the server rather than from a google-services.json so that the public repository names nobody's
 * Firebase project.
 */
@Serializable
data class PushOptions(
    @SerialName("project_id") val projectId: String = "",
    @SerialName("application_id") val applicationId: String = "",
    @SerialName("api_key") val apiKey: String = "",
    @SerialName("sender_id") val senderId: String = "",
) {
    val usable: Boolean
        get() = listOf(projectId, applicationId, apiKey, senderId).all { it.isNotBlank() }
}

/** The decisions about push, apart from Firebase so they can be tested. */
object PushPolicy {
    /**
     * FR-26.2: a resting phone's poll while it cannot rely on a push — the owner's five minutes. Twelve
     * wake-ups an hour where the stream's keepalive made 180.
     */
    const val POLL_WITHOUT_PUSH_MILLIS = 5 * 60 * 1000L

    /**
     * FR-26.3: the safety poll once a push has been seen to arrive. It catches what a push can lose —
     * FCM keeps a message five minutes, and Google may drop one — and nothing else.
     */
    const val POLL_WITH_PUSH_MILLIS = 30 * 60 * 1000L

    /**
     * Thirty minutes only once a push has arrived for the token this phone holds. A token is a promise
     * of delivery, not delivery: Google Play services can be disabled, restricted or behind a network
     * that drops its connection, and a phone that trusted the token alone would then hear of a
     * parent's change up to half an hour late with nothing to say why. The server pushes once when it
     * receives a new token, so a working phone proves itself within seconds.
     */
    fun pollMillis(token: String, verifiedToken: String): Long =
        if (token.isNotEmpty() && token == verifiedToken) POLL_WITH_PUSH_MILLIS else POLL_WITHOUT_PUSH_MILLIS

    enum class Plan { KEEP, START, RESTART, STOP }

    /** What to do with Firebase, given the options it runs with now and the ones the server sent. */
    fun plan(running: PushOptions?, wanted: PushOptions?): Plan {
        val want = wanted?.takeIf { it.usable }
        return when {
            running == null && want == null -> Plan.KEEP
            running == null -> Plan.START
            want == null -> Plan.STOP
            running == want -> Plan.KEEP
            else -> Plan.RESTART
        }
    }

    /**
     * The heartbeat's `push_token`: the token while the server sends push, else null, which leaves
     * what the server holds. Never "" from here — a phone that has not fetched its token yet must not
     * clear one the server can still use.
     */
    fun reportedToken(options: PushOptions?, token: String): String? =
        token.takeIf { options?.usable == true && it.isNotEmpty() }
}
