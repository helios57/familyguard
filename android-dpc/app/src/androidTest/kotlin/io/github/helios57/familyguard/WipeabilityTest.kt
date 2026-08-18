package io.github.helios57.familyguard

import android.app.admin.DevicePolicyManager
import android.content.Context
import android.content.Intent
import android.os.UserManager
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.admin.BootReceiver
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * FR-2.3 / NFR-6 on a real device: the phone the child carries can always be wiped from the recovery
 * menu.
 *
 * Everything else in this repo argues about it from the source — the engine never emits
 * `no_factory_reset`, the planner filters it, the applier clears it. This is the one test that asks
 * the platform. It is the only place the whole chain is evaluated end to end, and the only place a
 * mistake somewhere else in it would show up as something other than a passing unit test.
 *
 * It refuses to run on a device that is not managed by this app rather than skipping. A skip here
 * would be a green in the layer that exists precisely because the other layers cannot see the
 * device.
 *
 * **Nothing here may apply `no_debugging_features`.** It switches `adb` off, which severs the
 * connection instrumentation runs over — measured: the emulator went `device` → `offline` mid-test
 * and the AVD had to be wiped. It is not in [EnforcementEngine.BASELINE_RESTRICTIONS] for a product
 * reason (see that constant), and this class only ever applies the baseline; a future instrumented
 * test that applies a full computed state has to reckon with it separately.
 */
@RunWith(AndroidJUnit4::class)
class WipeabilityTest {

    private lateinit var context: Context
    private lateinit var users: UserManager

    private fun inEffect(): Set<String> {
        val bundle = users.userRestrictions
        return bundle.keySet().filterTo(mutableSetOf()) { bundle.getBoolean(it) }
    }

    @Before
    fun requireDeviceOwner() {
        context = InstrumentationRegistry.getInstrumentation().targetContext
        users = context.getSystemService(UserManager::class.java)
        val dpm = context.getSystemService(DevicePolicyManager::class.java)
        assertTrue(
            "this test measures a managed device and this one is not managed by ${context.packageName}. " +
                "Provision it first:  adb shell dpm set-device-owner " +
                "${context.packageName}/.admin.AdminReceiver",
            dpm.isDeviceOwnerApp(context.packageName),
        )
    }

    /**
     * The state a device is in after `PolicyComplianceActivity` has run — which is what the
     * enrollment script leaves it in, and what a phone handed to a child is in.
     */
    @Test
    fun theProvisionedDeviceIsStillWipeable() {
        val manager = DpmRestrictionGateway.hardeningManager(context)
        assertNotNull("the gateway could not bind to this device", manager)
        val outcome = manager!!.applyBaseline()

        // The positive control, and it has to be first: an assertion that `no_factory_reset` is
        // absent passes on a device where nothing at all was applied, which is the exact state a
        // broken gateway or a wrong ComponentName would produce.
        val effective = inEffect()
        for (restriction in EnforcementEngine.BASELINE_RESTRICTIONS) {
            assertTrue(
                "the baseline is not in effect ($restriction missing), so the check below would " +
                    "pass vacuously — outcome was $outcome",
                restriction in effective,
            )
        }
        for (forbidden in EnforcementEngine.FORBIDDEN_RESTRICTIONS) {
            assertFalse("the device has $forbidden in effect — outcome was $outcome", forbidden in effective)
        }
    }

    /**
     * The boot path, exercised through the receiver itself rather than around it.
     *
     * `tests/android/instrumented.sh` also reboots the device for real and runs this class again;
     * this test is what makes the failure legible when that one goes red, by separating "the boot
     * receiver applies the wrong thing" from "the device did not come back".
     */
    @Test
    fun theBootPathLeavesTheDeviceWipeable() {
        BootReceiver().onReceive(context, Intent(Intent.ACTION_BOOT_COMPLETED))

        val effective = inEffect()
        // Named rather than counted. "the boot path applied nothing" was the message here, and when
        // the platform silently dropped exactly one restriction it sent the reader looking for a
        // receiver that never fired instead of at the key it would not accept.
        val missing = EnforcementEngine.BASELINE_RESTRICTIONS.filter { it !in effective }
        assertTrue(
            "the boot path left the baseline incomplete: $missing is not in effect",
            missing.isEmpty(),
        )
        for (forbidden in EnforcementEngine.FORBIDDEN_RESTRICTIONS) {
            assertFalse("the boot path left $forbidden in effect", forbidden in effective)
        }
    }

    /**
     * A broadcast this receiver is not registered for must not be treated as a boot.
     *
     * Cheap, and it binds the action check: without it the receiver would re-harden on any intent
     * that reached it, which is a different bug from the one the test above covers and would be
     * invisible to it.
     *
     * **It asserts over what actually cleared, not over the whole baseline, and that is not a
     * weakening.** `UserManager.getUserRestrictions()` returns the *effective* set — the union of
     * what a device owner set and what the platform set for itself — and a device owner can only
     * clear its own half. Measured on API 29: provisioning a device owner leaves `no_add_user` as a
     * **base** restriction, which `dumpsys user` lists under `Restrictions:` rather than under
     * `Device policy local restrictions:`, and no `clearUserRestriction` removes it. So "every
     * baseline restriction is absent after clearing" is not a property this API level allows, and a
     * test demanding it fails for a reason that has nothing to do with the receiver under test.
     *
     * What the receiver has to be shown to do is *nothing*, so the subject is the delta: whatever
     * genuinely cleared must still be clear afterwards. The precondition survives in the form that
     * can actually be satisfied — at least one restriction has to have cleared, or a re-apply would
     * be invisible and a green here would mean nothing.
     */
    @Test
    fun aNonBootBroadcastIsIgnored() {
        val manager = DpmRestrictionGateway.hardeningManager(context)!!
        manager.apply(emptyList())

        val live = inEffect()
        val cleared = EnforcementEngine.BASELINE_RESTRICTIONS.filter { it !in live }
        assertTrue(
            "nothing cleared — the platform holds ${EnforcementEngine.BASELINE_RESTRICTIONS.filter { it in live }} " +
                "and a re-apply would therefore be undetectable, so the next assertion proves nothing",
            cleared.isNotEmpty(),
        )

        BootReceiver().onReceive(context, Intent(Intent.ACTION_USER_PRESENT))
        val after = inEffect()
        assertTrue(
            "a non-boot broadcast re-applied ${cleared.filter { it in after }}",
            cleared.none { it in after },
        )

        // Left as found: every other test in this class asserts the baseline is in effect, and a
        // test that depends on running before this one is a flake waiting for a reordered run.
        val restored = manager.applyBaseline()
        assertTrue(
            "this test could not put the device back the way it found it — $restored",
            EnforcementEngine.BASELINE_RESTRICTIONS.none { it !in inEffect() },
        )
    }
}
