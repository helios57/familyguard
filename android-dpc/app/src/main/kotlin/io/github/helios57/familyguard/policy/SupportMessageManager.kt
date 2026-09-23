package io.github.helios57.familyguard.policy

/**
 * The one line of text Android shows a child who taps an app this system has paused (FR-3.10).
 *
 * A paused app opens Android's own "blocked by your administrator" screen, not anything of this
 * app's: the platform routes an admin suspension there and never to a DPC's activity, so there is
 * no button a DPC can add. The short support message is the only text on it that a DPC controls —
 * which makes it the place a child reads WHY, at the moment they wonder.
 */
interface SupportMessageGateway {
    fun get(): String?
    fun set(text: String)
}

data class SupportMessageOutcome(val summary: String, val failure: String? = null)

class SupportMessageManager(private val gateway: SupportMessageGateway) {

    /** Sets [text] unless it is already the message, and reads it back either way. */
    fun apply(text: String): SupportMessageOutcome {
        val wanted = text.take(MAX_CHARS)
        val current = try {
            gateway.get()
        } catch (e: RuntimeException) {
            return SupportMessageOutcome("support message", e.message ?: e.javaClass.simpleName)
        }
        if (current == wanted) return SupportMessageOutcome("support message unchanged")
        try {
            gateway.set(wanted)
        } catch (e: RuntimeException) {
            return SupportMessageOutcome("support message", e.message ?: e.javaClass.simpleName)
        }
        // setShortSupportMessage returns nothing; the read-back is the only evidence it took.
        val after = try {
            gateway.get()
        } catch (e: RuntimeException) {
            return SupportMessageOutcome("support message", e.message ?: e.javaClass.simpleName)
        }
        return if (after == wanted) SupportMessageOutcome("support message set")
        else SupportMessageOutcome("support message", "accepted, and the device reports ${after?.take(40)}")
    }

    companion object {
        /** The platform documents truncation past 200 characters; this never relies on it. */
        const val MAX_CHARS = 200
    }
}
