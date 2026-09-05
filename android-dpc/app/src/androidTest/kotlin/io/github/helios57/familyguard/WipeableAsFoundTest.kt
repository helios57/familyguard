package io.github.helios57.familyguard

import android.app.admin.DevicePolicyManager
import android.os.UserManager
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.enforce.EnforcementEngine
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Reads the device's state and changes nothing.
 *
 * This is the test `tests/android/instrumented.sh` runs **after rebooting the phone**, and it is a
 * separate class from [WipeabilityTest] for one reason: that one applies the baseline before
 * asserting, so running it after a reboot would prove the applier still works and say nothing at all
 * about whether the boot receiver ran. What is being measured here is the state the boot left
 * behind.
 *
 * Run on its own against a device that was provisioned and never told to harden, this fails — and
 * that is right. A device owner with no baseline in effect is a real defect state, not a test
 * environment problem. `tests/android/instrumented.sh` is what sequences the two passes.
 */
@RunWith(AndroidJUnit4::class)
@SequencedByAScript
class WipeableAsFoundTest {

    @Test
    fun theDeviceIsWipeableInWhateverStateItWasFound() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val dpm = context.getSystemService(DevicePolicyManager::class.java)
        assertTrue(
            "this device is not managed by ${context.packageName}; there is nothing here to measure",
            dpm.isDeviceOwnerApp(context.packageName),
        )

        val bundle = context.getSystemService(UserManager::class.java).userRestrictions
        val effective = bundle.keySet().filterTo(mutableSetOf()) { bundle.getBoolean(it) }

        // The positive control. A device where nothing is restricted passes every absence check
        // below while proving nothing — and "the baseline did not survive the reboot" is itself the
        // failure this pass exists to catch.
        val missing = EnforcementEngine.BASELINE_RESTRICTIONS.filter { it !in effective }
        assertTrue(
            "the baseline is not in effect on this device (missing $missing). If this ran straight " +
                "after provisioning, run tests/android/instrumented.sh instead — it sequences the " +
                "apply pass before this one. If it ran after a reboot, the boot receiver did not.",
            missing.isEmpty(),
        )

        for (forbidden in EnforcementEngine.FORBIDDEN_RESTRICTIONS) {
            assertFalse(
                "$forbidden is in effect: this phone can no longer be reset from recovery (FR-2.3)",
                forbidden in effective,
            )
        }
    }
}
