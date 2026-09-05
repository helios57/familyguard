package io.github.helios57.familyguard

import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.UserManager
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.admin.BootReceiver
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * FR-2.3 / NFR-6, from the other side: not "the DPC never sets `no_factory_reset`" but "a phone that
 * has it set gets it taken back off".
 *
 * [WipeabilityTest] and [WipeableAsFoundTest] assert that the restriction is **absent**, and an
 * absence assertion is only worth what its positive control is worth. Theirs proves the baseline
 * landed, which shows the gateway works — it does not show that this particular key would be
 * *visible* if it were in effect. That gap is not hypothetical on this codebase: the restriction
 * constants are hand-copied strings, the platform accepts an unknown one silently, and
 * [EnforcementEngine.RESTRICTION_PRIVATE_DNS] was measurably wrong for exactly that reason. A
 * misspelled `no_factory_reset` would make every wipeability assertion in this repo pass forever
 * while the real restriction went unpoliced.
 *
 * So this class sets it on purpose, reads it back from `UserManager`, and only then asks the DPC to
 * deal with it. The first assertion is the control the other tests cannot carry; the rest is the
 * recovery path a real phone depends on — [io.github.helios57.familyguard.policy.RestrictionPlanner.floor]
 * puts every forbidden restriction into its `clear` list, and that runs at provisioning compliance
 * and on **every boot**.
 *
 * It is the only test in this repo that deliberately makes the device un-wipeable, so leaving it
 * that way is the one failure mode that would be worse than the bug it looks for. [@After] clears
 * the restriction whatever happened above and asserts the device came back — it is a safety net and
 * an assertion at once, and it runs even when the test body has already failed.
 */
@RunWith(AndroidJUnit4::class)
class FactoryResetRecoveryTest {

    private val forbidden = EnforcementEngine.RESTRICTION_FACTORY_RESET

    private lateinit var context: Context
    private lateinit var users: UserManager
    private lateinit var gateway: DpmRestrictionGateway

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
        gateway = DpmRestrictionGateway(
            dpm,
            ComponentName(context.packageName, DpmRestrictionGateway.ADMIN_RECEIVER),
            users,
        )
    }

    /**
     * Not `@After` decoration — this is the last measurement, and it is the one that must never be
     * skipped. A test that ends having left `no_factory_reset` on the device has done the damage it
     * exists to prevent.
     */
    @After
    fun theDeviceIsLeftWipeable() {
        runCatching { gateway.clear(forbidden) }
        assertFalse(
            "this test left $forbidden in effect on the device — it can no longer be reset from " +
                "Settings. Clear it by hand:  adb shell dpm remove-active-admin " +
                "${context.packageName}/.admin.AdminReceiver  (or wipe the AVD)",
            forbidden in inEffect(),
        )
    }

    /**
     * The provisioning-compliance path, which is what runs when a phone is enrolled.
     */
    @Test
    fun theBaselineTakesBackAFactoryResetBlockItFinds() {
        val manager = DpmRestrictionGateway.hardeningManager(context)
        assertNotNull("the gateway could not bind to this device", manager)

        gateway.add(forbidden)

        // The positive control, and the reason this class exists. If the platform does not report
        // the restriction after a device owner set it, then every "no_factory_reset is absent"
        // assertion in this repo is passing vacuously and none of them is evidence of anything.
        assertTrue(
            "the platform does not report $forbidden as in effect immediately after this device " +
                "owner set it. Either the key is misspelled — in which case every wipeability " +
                "assertion in this repo is vacuous — or this build cannot set restrictions at all. " +
                "In effect right now: ${inEffect().sorted()}",
            forbidden in inEffect(),
        )

        val outcome = manager!!.applyBaseline()

        assertTrue(
            "the baseline ran and did not take $forbidden back off — outcome was $outcome",
            outcome.stillForbidden.isEmpty(),
        )
        assertTrue(
            "$forbidden did not appear in the plan's clear list, so the baseline removed it by " +
                "accident rather than on purpose — outcome was $outcome",
            forbidden in outcome.cleared,
        )
        assertFalse(
            "the platform still reports $forbidden after the baseline cleared it — outcome was $outcome",
            forbidden in inEffect(),
        )
    }

    /**
     * The same recovery through the receiver a real phone actually runs, on the schedule a real
     * phone actually runs it: every boot, before and independently of any contact with the server.
     *
     * This is the property the owner is relying on when the control plane is unreachable, the policy
     * is wrong, or the app is broken — reboot the phone and the reset menu comes back.
     */
    @Test
    fun aBootTakesBackAFactoryResetBlockItFinds() {
        gateway.add(forbidden)
        assertTrue(
            "the platform does not report $forbidden after this device owner set it; the assertion " +
                "below would pass vacuously. In effect right now: ${inEffect().sorted()}",
            forbidden in inEffect(),
        )

        BootReceiver().onReceive(context, Intent(Intent.ACTION_BOOT_COMPLETED))

        assertFalse(
            "a boot left $forbidden in effect: this phone could not be reset from Settings and " +
                "rebooting it would not help (FR-2.3). In effect: ${inEffect().sorted()}",
            forbidden in inEffect(),
        )
    }
}
