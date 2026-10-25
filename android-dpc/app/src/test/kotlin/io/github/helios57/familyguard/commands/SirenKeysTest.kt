package io.github.helios57.familyguard.commands

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-9.4: any button stops the siren. The cases are the ones a phone actually produces — the Stopp
 * screen turning the display on by itself, a display timing out, the side key with the display on
 * and with it off, and a volume key on the Stopp screen.
 */
class SirenKeysTest {

    private val start = 1_000_000L

    @Test
    fun screenOnInsideGraceIsIgnored() {
        // The Stopp screen turns the display on itself, within moments of the siren starting. That
        // is not somebody pressing a button, and stopping on it would silence every siren at once.
        val keys = SirenKeys(start)
        assertFalse(keys.onScreenOn(start + 1_999))
    }

    @Test
    fun screenOnAfterGraceStops() {
        val keys = SirenKeys(start)
        assertTrue(keys.onScreenOn(start + 2_000))
    }

    @Test
    fun screenOffByTimeoutIsIgnored() {
        // With no Stopp screen holding the display on, the display going off may be its own timeout.
        val keys = SirenKeys(start)
        assertFalse(keys.onScreenOff(start + 30_000, keptOnByUs = false))
    }

    @Test
    fun screenOffWhileKeptOnStops() {
        // The Stopp screen holds the display on, so the only thing that turns it off is the side key.
        val keys = SirenKeys(start)
        assertTrue(keys.onScreenOff(start + 500, keptOnByUs = true))
    }

    @Test
    fun anyKeyStops() {
        // A key reaches the Stopp screen only from a person's finger, so there is no grace for it.
        assertTrue(SirenKeys(start).onKey(start))
        assertTrue(SirenKeys(start).onKey(start + 60_000))
    }

    @Test
    fun onlyTheFirstPressStops() {
        // A press turns the display off and the next turns it on again; the second must not send a
        // second stop to a siren that is no longer ringing.
        val keys = SirenKeys(start)
        assertTrue(keys.onScreenOff(start + 5_000, keptOnByUs = true))
        assertFalse(keys.onScreenOn(start + 6_000))
        assertFalse(keys.onKey(start + 7_000))
    }
}

/**
 * When the Stopp screen holds the display on (FR-9.4): while it is visible, and a moment after it stops
 * — the side key stops it just before the display-off broadcast arrives. Covered for longer, a display
 * going off is its own timeout, not a press.
 */
class ScreenHoldTest {

    @Test
    fun neverShownHoldsNothing() {
        assertFalse(ScreenHold().holding(nowMs = 1_000))
    }

    @Test
    fun visibleHoldsTheDisplay() {
        val hold = ScreenHold()
        hold.onStart()
        assertTrue(hold.holding(nowMs = 60_000))
    }

    @Test
    fun justStoppedStillHoldsSoTheSideKeysDisplayOffIsAPress() {
        val hold = ScreenHold(tailMs = 3_000)
        hold.onStart()
        hold.onStop(nowMs = 10_000)
        assertTrue(hold.holding(nowMs = 12_999))
    }

    @Test
    fun coveredLongerThanTheTailHoldsNothingSoATimeoutIsNotAPress() {
        val hold = ScreenHold(tailMs = 3_000)
        hold.onStart()
        hold.onStop(nowMs = 10_000)
        assertFalse(hold.holding(nowMs = 25_000))
        assertFalse(SirenKeys(startedAtMs = 0).onScreenOff(25_000, hold.holding(25_000)))
    }

    @Test
    fun shownAgainHoldsAgain() {
        val hold = ScreenHold(tailMs = 3_000)
        hold.onStart()
        hold.onStop(nowMs = 10_000)
        hold.onStart()
        assertTrue(hold.holding(nowMs = 60_000))
    }
}
