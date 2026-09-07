package io.github.helios57.familyguard.commands

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * @param volume the alarm stream's level, or null for a device that will not say.
 * @param throwOn the calls that fail, by name — each of them is a real platform failure mode
 * (no default alarm sound, no vibrator, a stream the OEM will not let an app change).
 */
private class FakeSirenDevice(
    var volume: Int? = 3,
    private val maxVolume: Int = 7,
    private val throwOn: Set<String> = emptySet(),
) : SirenDevice {
    var tonePlaying = false
        private set
    var vibrating = false
        private set
    var toneStarts = 0
        private set

    /** Whether a person holding this phone has a way to silence it. */
    var stopControlShown = false
        private set
    var stopControlHides = 0
        private set
    val volumesSet = mutableListOf<Int>()

    private fun maybeThrow(call: String) {
        if (call in throwOn) throw IllegalStateException("$call is not available")
    }

    override fun startTone() {
        maybeThrow("startTone")
        tonePlaying = true
        toneStarts++
    }

    override fun stopTone() {
        maybeThrow("stopTone")
        tonePlaying = false
    }

    override fun startVibration() {
        maybeThrow("startVibration")
        vibrating = true
    }

    override fun stopVibration() {
        maybeThrow("stopVibration")
        vibrating = false
    }

    override fun showStopControl() {
        maybeThrow("showStopControl")
        stopControlShown = true
    }

    override fun hideStopControl() {
        maybeThrow("hideStopControl")
        stopControlShown = false
        stopControlHides++
    }

    override fun alarmVolume(): Int? {
        maybeThrow("alarmVolume")
        return volume
    }

    override fun maxAlarmVolume(): Int {
        maybeThrow("maxAlarmVolume")
        return maxVolume
    }

    override fun setAlarmVolume(level: Int) {
        maybeThrow("setAlarmVolume")
        volumesSet += level
        volume = level
    }
}

/** A timer that holds one pending action, so a test can be the five minutes passing. */
private class FakeSirenTimer : SirenTimer {
    var pending: (() -> Unit)? = null
        private set
    var armedDelay: Long? = null
        private set
    var arms = 0
        private set
    var cancels = 0
        private set

    override fun arm(delayMillis: Long, action: () -> Unit) {
        arms++
        armedDelay = delayMillis
        pending = action
    }

    override fun cancel() {
        cancels++
        pending = null
    }

    /** What the phone does when no `STOP_ALARM` ever arrives. */
    fun fire() = pending?.invoke()
}

class SirenControllerTest {

    @Test
    fun `starting rings, raises the alarm volume, vibrates and arms the deadline`() {
        val device = FakeSirenDevice(volume = 3, maxVolume = 7)
        val timer = FakeSirenTimer()

        val outcome = SirenController(device, timer).start()

        assertTrue(outcome.toString(), outcome.ok)
        assertNull(outcome.note)
        assertTrue(device.tonePlaying)
        assertTrue(device.vibrating)
        assertEquals(listOf(7), device.volumesSet)
        assertEquals(SirenController.DEFAULT_MAX_DURATION_MILLIS, timer.armedDelay)
    }

    @Test
    fun `it silences itself when no stop ever arrives`() {
        val device = FakeSirenDevice(volume = 3)
        val timer = FakeSirenTimer()
        val siren = SirenController(device, timer)
        siren.start()

        // The child walked into a lift. The parent's STOP_ALARM is queued on a server the phone
        // cannot see, so the only thing that can end this is the phone itself.
        timer.fire()

        assertFalse(siren.isRinging())
        assertFalse(device.tonePlaying)
        assertFalse(device.vibrating)
        assertEquals(3, device.volume)
    }

    @Test
    fun `a second trigger extends the deadline and does not restart the tone`() {
        val device = FakeSirenDevice(volume = 3, maxVolume = 7)
        val timer = FakeSirenTimer()
        val siren = SirenController(device, timer)
        siren.start()

        val outcome = siren.start()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(1, device.toneStarts)
        assertEquals(2, timer.arms)
        // The volume is captured at the FIRST start only. Re-capturing here would save the maximum
        // this class had just set and restore *that* as the child's alarm volume forever.
        assertEquals(listOf(7), device.volumesSet)
        siren.stop()
        assertEquals(3, device.volume)
    }

    @Test
    fun `stopping restores the volume that was there before`() {
        val device = FakeSirenDevice(volume = 2, maxVolume = 7)
        val siren = SirenController(device, FakeSirenTimer())
        siren.start()

        val outcome = siren.stop()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(2, device.volume)
        assertFalse(device.tonePlaying)
        assertFalse(device.vibrating)
    }

    @Test
    fun `an unreadable volume is said out loud and never guessed`() {
        val device = FakeSirenDevice(volume = null, maxVolume = 7)
        val siren = SirenController(device, FakeSirenTimer())

        val outcome = siren.start()

        assertTrue(outcome.toString(), outcome.ok)
        val note = requireNotNull(outcome.note) { "expected a note, got $outcome" }
        assertTrue(note, note.contains("could not be read"))

        siren.stop()
        // Restoring a fabricated 0 would leave the child's alarm clock silent tomorrow morning, and
        // restoring a fabricated anything-else is the same class of lie.
        assertEquals(listOf(7), device.volumesSet)
    }

    @Test
    fun `a volume that cannot be raised is a note, not a failure`() {
        val device = FakeSirenDevice(volume = 3, throwOn = setOf("setAlarmVolume"))
        val siren = SirenController(device, FakeSirenTimer())

        val outcome = siren.start()

        // A siren at whatever volume the phone was already on is worth far more than no siren.
        assertTrue(outcome.toString(), outcome.ok)
        assertTrue(device.tonePlaying)
        val note = requireNotNull(outcome.note) { "expected a note, got $outcome" }
        assertTrue(note, note.contains("current volume"))
    }

    @Test
    fun `a vibrator that throws still leaves a siren that stop can reach`() {
        val device = FakeSirenDevice(volume = 3, throwOn = setOf("startVibration"))
        val siren = SirenController(device, FakeSirenTimer())

        val outcome = siren.start()

        assertTrue(outcome.toString(), outcome.ok)
        assertTrue(device.tonePlaying)
        // The tone is already playing. An object that thought it was silent would make the parent's
        // STOP_ALARM a no-op on a phone that is audibly ringing.
        assertTrue(siren.isRinging())
        val note = requireNotNull(outcome.note) { "expected a note, got $outcome" }
        assertTrue(note, note.contains("not vibrating"))

        assertTrue(siren.stop().ok)
        assertFalse(device.tonePlaying)
    }

    @Test
    fun `a tone that will not start is a failure, and puts the volume back`() {
        val device = FakeSirenDevice(volume = 3, maxVolume = 7, throwOn = setOf("startTone"))
        val timer = FakeSirenTimer()
        val siren = SirenController(device, timer)

        val outcome = siren.start()

        assertFalse(outcome.toString(), outcome.ok)
        assertFalse(siren.isRinging())
        // Raised, then put back: the parent gets a failure and the child's phone is left as it was.
        assertEquals(listOf(7, 3), device.volumesSet)
        assertEquals(0, timer.arms)
    }

    @Test
    fun `stopping a siren that already timed out is not an error`() {
        val device = FakeSirenDevice(volume = 3)
        val timer = FakeSirenTimer()
        val siren = SirenController(device, timer)
        siren.start()
        timer.fire()

        val outcome = siren.stop()

        // Answering this as a failure would show the parent an error for the outcome they asked for.
        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("not ringing", outcome.summary)
    }

    @Test
    fun `stopping is idempotent and clears the siren even when the platform throws`() {
        val device = FakeSirenDevice(volume = 3, throwOn = setOf("stopTone", "stopVibration"))
        val siren = SirenController(device, FakeSirenTimer())
        siren.start()

        val first = siren.stop()

        assertFalse(first.toString(), first.ok)
        val failure = requireNotNull(first.failure) { "expected a failure, got $first" }
        assertTrue(failure, failure.contains("still vibrating"))
        // Left ringing, the next stop is a no-op and the next start skips the tone — which is how a
        // phone ends up silent when a parent presses the siren.
        assertFalse(siren.isRinging())
        // Restored anyway: the alternative pins the alarm stream at maximum for every notification
        // from now on.
        assertEquals(3, device.volume)
        assertTrue(siren.stop().ok)
    }

    @Test
    fun `the deadline is cancelled when a stop arrives`() {
        val timer = FakeSirenTimer()
        val siren = SirenController(FakeSirenDevice(), timer)
        siren.start()

        siren.stop()

        assertEquals(1, timer.cancels)
        assertNull(timer.pending)
    }

    /**
     * The defect this control was added for, held as one test.
     *
     * On 2026-09-07 a parent rang the pilot phone and then could not stop it — the console had no
     * STOP_ALARM button and the handset offered nothing at all — so it ran its full five-minute cap
     * while they watched. Volume-down cannot help: the tone plays on the alarm stream at maximum,
     * and nothing about volume reaches the vibrator, which repeats forever by construction.
     */
    @Test
    fun `a ringing phone carries its own way to be silenced`() {
        val device = FakeSirenDevice()
        val controller = SirenController(device, FakeSirenTimer())

        controller.start()
        assertTrue("a phone that is ringing must offer a stop", device.stopControlShown)

        controller.stop()
        assertFalse("a phone that has stopped must not still offer one", device.stopControlShown)
    }

    /**
     * The auto-stop path, which is the one no `STOP_ALARM` runs through.
     *
     * A control left on screen by the cap would be a phone that looks like it is still ringing and
     * a button that does nothing — indistinguishable, to the person holding it, from the original
     * defect.
     */
    @Test
    fun `the five-minute cap takes the stop control away with the tone`() {
        val device = FakeSirenDevice()
        val timer = FakeSirenTimer()
        val controller = SirenController(device, timer)

        controller.start()
        assertTrue(device.stopControlShown)

        timer.fire()

        assertFalse("the cap stopped the tone", device.tonePlaying)
        assertFalse("but left the control behind", device.stopControlShown)
    }

    /**
     * A notification outlives the process that posted it, so a service killed mid-siren comes back
     * believing nothing is ringing while the phone still shows a stop control. `STOP_ALARM` has to
     * be the answer to that too — the alternative is a control that survives every attempt to
     * remove it.
     */
    @Test
    fun `stopping a siren that is not ringing still clears a control left behind`() {
        val device = FakeSirenDevice()
        val controller = SirenController(device, FakeSirenTimer())

        val outcome = controller.stop()

        assertEquals("not ringing", outcome.summary)
        assertTrue("the not-ringing path must still clear it", device.stopControlHides >= 1)
        assertFalse(device.stopControlShown)
    }

    /**
     * A siren nobody can see a stop for is still a siren, and it still ends by itself. Reported as
     * a note so the parent is told which of the two they got — the same shape as a phone with no
     * vibrator.
     */
    @Test
    fun `a phone that cannot show the control still rings, and says so`() {
        val device = FakeSirenDevice(throwOn = setOf("showStopControl"))
        val controller = SirenController(device, FakeSirenTimer())

        val outcome = controller.start()

        assertTrue("the tone is the feature; the control is not", device.tonePlaying)
        assertTrue("this is not a failed command", outcome.ok)
        assertTrue(
            "the parent is not told the phone has no stop control: ${outcome.note}",
            outcome.note?.contains("no stop control") == true,
        )
    }

    /**
     * The other direction, and it is a failure rather than a note: a control left on screen for a
     * siren that has stopped is a button that does nothing, which is the symptom of the defect.
     */
    @Test
    fun `a control that cannot be taken away is reported as a failure`() {
        val device = FakeSirenDevice(throwOn = setOf("hideStopControl"))
        val controller = SirenController(device, FakeSirenTimer())
        controller.start()

        val outcome = controller.stop()

        assertFalse("this must not be reported as a clean stop", outcome.ok)
        assertTrue(
            "the failure does not name the control: ${outcome.failure}",
            outcome.failure?.contains("stop control") == true,
        )
        // Still stopped everything it could reach. A siren that gives up on the tone because a
        // notification would not cancel is worse than the notification.
        assertFalse(device.tonePlaying)
        assertFalse(device.vibrating)
    }

    /**
     * The negative control for the four above.
     *
     * A siren that failed to start must not leave a stop control for a tone that is not playing.
     * Without this, `showStopControl` could be called unconditionally at the top of `start()` and
     * every other test here would stay green.
     */
    @Test
    fun `a siren that could not start shows no control at all`() {
        val device = FakeSirenDevice(throwOn = setOf("startTone"))
        val controller = SirenController(device, FakeSirenTimer())

        val outcome = controller.start()

        assertFalse("the tone never started", outcome.ok)
        assertFalse("so there is nothing to stop", device.stopControlShown)
    }
}
