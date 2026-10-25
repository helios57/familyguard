package io.github.helios57.familyguard.commands

/**
 * Whether a button press should stop a ringing siren (FR-9.4): a person who has found the phone
 * presses whatever is under their thumb, and that has to be enough.
 *
 * An app is told about only some of a phone's buttons, and each of the rest shows up indirectly:
 * - a volume key reaches the Stopp screen as a key event — [onKey];
 * - the side key with the display off shows up as the display turning on — [onScreenOn];
 * - the side key with the display on shows up as the display turning off — [onScreenOff].
 *
 * The display also turns on and off for reasons that are not a press, and those are what the rules
 * here exclude. The Stopp screen turns the display on itself as the siren starts, so a display
 * turning on within [graceMs] of the start is not a press. And a display can go off by its own
 * timeout, so that counts only while the Stopp screen is holding the display on — then the side key
 * is the only thing that can turn it off.
 *
 * Only the first press answers true: the side key turns the display off and the next press turns it
 * on again, and the siren has already been stopped by the first.
 *
 * Pure, so that every one of those cases runs on the JVM; [AndroidSirenDevice] feeds it the platform
 * events and [SirenActivity] the key events.
 */
class SirenKeys(private val startedAtMs: Long, private val graceMs: Long = 2_000) {

    private var stopped = false

    /** A key on the Stopp screen. Only a finger produces one, so there is no grace. */
    @Synchronized
    fun onKey(nowMs: Long): Boolean = stop()

    @Synchronized
    fun onScreenOn(nowMs: Long): Boolean = nowMs - startedAtMs >= graceMs && stop()

    /** @param keptOnByUs whether the Stopp screen was holding the display on when it went off. */
    @Synchronized
    fun onScreenOff(nowMs: Long, keptOnByUs: Boolean): Boolean = keptOnByUs && stop()

    private fun stop(): Boolean {
        if (stopped) return false
        stopped = true
        return true
    }
}
