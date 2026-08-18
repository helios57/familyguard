package io.github.helios57.familyguard.policy

/** The three keyguard facts locking needs, named so the decision can be tested without a phone. */
interface LockGateway {
    /** Turns the screen off and shows the keyguard. */
    fun lockNow()

    /** Whether this device has a PIN, pattern or password — i.e. whether the keyguard holds. */
    fun deviceSecure(): Boolean

    /** Whether the keyguard is showing and locked at this moment. */
    fun deviceLocked(): Boolean
}

data class LockOutcome(val summary: String, val failure: String? = null, val note: String? = null) {
    val ok: Boolean get() = failure == null
}

/**
 * `LOCK_NOW`, and the standing parent lock it sets (FR-9).
 *
 * Three things about this are worth stating, because each has an obvious wrong version:
 *
 * **A device with no lock-screen credential cannot be locked, and this says so.** `lockNow` succeeds
 * on such a phone — the screen goes off — and the child swipes straight back in. The platform
 * reports nothing; the only place that fact exists is `isDeviceSecure`. Without this check the
 * console would show a locked device the child is using, which is worse than showing a failure,
 * because a failure is something a parent can act on.
 *
 * **It does not re-lock a device that is already locked.** The parent lock is a standing flag, so
 * this runs on every sync and every push for as long as it is set. Calling `lockNow` each time would
 * blank the screen of a phone that is sitting locked in a bag every fifteen minutes for no gain.
 *
 * **There is no unlock.** `UNLOCK_DEVICE` clears the flag on the server, and the device's part is
 * simply to stop re-locking — there is no platform call that dismisses a keyguard, and the two that
 * look like one (`resetPassword`, `setKeyguardDisabled`) are respectively refused for a device owner
 * since API 26 and permitted only on a device that has no credential to begin with. A handler that
 * pretended otherwise would acknowledge an unlock that never happened.
 */
class LockManager(private val gateway: LockGateway) {

    fun lock(): LockOutcome {
        val notes = mutableListOf<String>()

        // Null means unreadable, which is neither secure nor insecure. It is carried as a note: a
        // guarantee that could not be checked must not be reported as a guarantee that holds, and it
        // must not be reported as one that fails either.
        val secure = read { gateway.deviceSecure() }
        val failure = when (secure) {
            false -> "the parent lock cannot hold: this device has no PIN, pattern or password, so " +
                "the keyguard is dismissed with a swipe"
            null -> null.also { notes += "whether this device has a lock-screen credential could not be read" }
            true -> null
        }

        if (read { gateway.deviceLocked() } == true) {
            return LockOutcome("already locked", failure, notes.joinToString("; ").ifEmpty { null })
        }

        try {
            gateway.lockNow()
        } catch (e: RuntimeException) {
            return LockOutcome("lock", failure = e.message ?: e.javaClass.simpleName)
        }

        // Read back, and reported rather than asserted. The keyguard is raised asynchronously, so a
        // `false` here is at least as likely to mean "not yet" as "did not work" — turning that into
        // a failure would make a working lock report a problem on some hardware and not others, and
        // a problem is what stops the device claiming its policy version.
        if (read { gateway.deviceLocked() } == false) {
            notes += "the keyguard had not come up yet when this was read back"
        }
        return LockOutcome("locked", failure, notes.joinToString("; ").ifEmpty { null })
    }

    private fun read(probe: () -> Boolean): Boolean? = try {
        probe()
    } catch (_: RuntimeException) {
        null
    }
}
