package io.github.helios57.familyguard.commands

/**
 * The noise-making half of `TRIGGER_ALARM` (FR-9), named so the decision can be tested without a
 * phone.
 *
 * The tone plays on the **alarm** stream, which is what makes it audible on a phone whose ringer is
 * silenced — the requirement's "overriding the silent switch". It does not override every Do Not
 * Disturb configuration: a DND rule that suppresses alarms still suppresses this, and the way past
 * that is notification-policy access, which is a special access a user grants in Settings and a
 * device owner cannot grant to itself. That limit is documented rather than worked around, because
 * the alternative is asking the child to grant it.
 */
interface SirenDevice {
    fun startTone()
    fun stopTone()

    /** Continuous vibration, for a phone that is face-down under a cushion. */
    fun startVibration()
    fun stopVibration()

    /**
     * The alarm stream's current level, or `null` when it cannot be read.
     *
     * Nullable because "the volume was not read" and "the volume is 0" are different facts, and
     * restoring a fabricated 0 would leave a child's alarm clock silent tomorrow morning.
     */
    fun alarmVolume(): Int?

    fun maxAlarmVolume(): Int
    fun setAlarmVolume(level: Int)
}

/** A single pending auto-stop. Arming replaces whatever was armed before. */
interface SirenTimer {
    fun arm(delayMillis: Long, action: () -> Unit)
    fun cancel()
}

/** What one siren call did, and anything the parent should know that is not a failure. */
data class SirenOutcome(val summary: String, val failure: String? = null, val note: String? = null) {
    val ok: Boolean get() = failure == null
}

/**
 * Rings, and — this is the part that matters — always stops.
 *
 * `TRIGGER_ALARM` and `STOP_ALARM` are two separate commands, which means the stop travels over the
 * same network the start did. A child who walks into a lift between them leaves a phone screaming in
 * a school bag with no way to reach it: the parent's stop is queued on a server the phone cannot
 * see. So the stop does not depend on the network at all — the siren carries its own deadline and
 * silences itself after [maxDurationMillis], and the delivered `STOP_ALARM` is the fast path rather
 * than the only path.
 *
 * Two decisions inside that:
 *
 *  - **A second `TRIGGER_ALARM` while ringing re-arms the deadline and does not restart the tone.**
 *    A parent walking through the house looking for the phone can press again and get another five
 *    minutes. This cannot run away: extending requires a command that arrives, and the failure this
 *    class exists for is a command that does not.
 *  - **The volume is captured at the *first* start only.** Re-capturing on the second would save the
 *    maximum this class had just set, and restore *that* as the child's alarm volume forever after.
 */
class SirenController(
    private val device: SirenDevice,
    private val timer: SirenTimer,
    private val maxDurationMillis: Long = DEFAULT_MAX_DURATION_MILLIS,
) {

    private var ringing = false

    /** The level to put back on stop. Null means it could not be read and must not be guessed. */
    private var restoreVolume: Int? = null

    fun isRinging(): Boolean = ringing

    fun start(): SirenOutcome {
        if (ringing) {
            timer.arm(maxDurationMillis) { autoStop() }
            return SirenOutcome("already ringing; auto-stop extended")
        }

        var note: String? = null
        // Read before the tone starts. Reading afterwards would still be correct today, but it puts
        // the one irreversible step — raising the volume — before the read that undoes it.
        restoreVolume = try {
            device.alarmVolume()
        } catch (e: RuntimeException) {
            null
        }
        if (restoreVolume == null) {
            note = "the alarm volume could not be read, so it will not be restored when the siren stops"
        }

        try {
            device.setAlarmVolume(device.maxAlarmVolume())
        } catch (e: RuntimeException) {
            // Not a failure. A siren at whatever volume the phone was already on is worth far more
            // than no siren, and the parent is told which of the two they got.
            note = "the alarm volume could not be raised (${e.message ?: e.javaClass.simpleName}); " +
                "the siren is playing at the device's current volume"
        }

        try {
            device.startTone()
        } catch (e: RuntimeException) {
            restoreVolume?.let { runCatching { device.setAlarmVolume(it) } }
            restoreVolume = null
            return SirenOutcome("siren", failure = e.message ?: e.javaClass.simpleName)
        }

        // Marked ringing before the vibration is attempted. A vibrator that throws must still leave a
        // siren that `stop()` can reach: the tone is already playing, and an object that thinks it is
        // silent is one whose `STOP_ALARM` is a no-op.
        ringing = true
        try {
            device.startVibration()
        } catch (e: RuntimeException) {
            note = "the phone is not vibrating (${e.message ?: e.javaClass.simpleName}); the tone is playing"
        }

        timer.arm(maxDurationMillis) { autoStop() }
        return SirenOutcome("ringing, auto-stop in ${maxDurationMillis / 1000}s", note = note)
    }

    /**
     * Silences it. Idempotent — a `STOP_ALARM` for a siren that already timed out is not an error,
     * and answering it as one would show the parent a failure for the outcome they asked for.
     */
    fun stop(): SirenOutcome {
        timer.cancel()
        if (!ringing) return SirenOutcome("not ringing")

        val failures = mutableListOf<String>()
        try {
            device.stopTone()
        } catch (e: RuntimeException) {
            failures += e.message ?: e.javaClass.simpleName
        }
        // Attempted even when the tone failed to stop, and reported as a failure rather than a note:
        // a vibrator left running is a battery this parent cannot reach and a phone that keeps
        // buzzing in a classroom.
        try {
            device.stopVibration()
        } catch (e: RuntimeException) {
            failures += "the phone is still vibrating (${e.message ?: e.javaClass.simpleName})"
        }
        // Restored even when stopping the tone threw. The alternative leaves a phone whose alarm
        // stream is pinned at maximum for every notification from now on.
        restoreVolume?.let { level ->
            try {
                device.setAlarmVolume(level)
            } catch (e: RuntimeException) {
                failures += "the alarm volume was left at maximum (${e.message ?: e.javaClass.simpleName})"
            }
        }

        // The siren is off as far as this object is concerned even if the platform threw: leaving
        // `ringing` true would make the next stop a no-op, and the next start skip the tone.
        ringing = false
        restoreVolume = null
        return if (failures.isEmpty()) SirenOutcome("stopped")
        else SirenOutcome("stopped", failure = failures.joinToString("; "))
    }

    private fun autoStop() {
        if (ringing) stop()
    }

    companion object {
        /**
         * Long enough to find a phone in a house, short enough that a lost `STOP_ALARM` is an
         * annoyance rather than something a child has to hide in a bag.
         */
        const val DEFAULT_MAX_DURATION_MILLIS = 5L * 60 * 1000
    }
}
