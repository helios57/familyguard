package io.github.helios57.familyguard

import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.os.UserManager
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.policy.DeviceOwnerPolicy
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import org.junit.After
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * FR-1.9 on a device: a system app setup removed for this user comes back on the next sync, and one
 * the policy hides stays away.
 *
 * Written for a family Samsung that had no Clock from its first inventory; that phone turned out not
 * to carry the Clock at all (FR-1.9), so this measures the repair, not that phone. Nothing in the
 * product can remove a system app for a user, so the state is made by
 * `tests/android/system-apps.sh` with root (`pm uninstall --user 0`), which leaves exactly the
 * platform record setup leaves: the package on the image, `installed=false` for user 0. That is why
 * this class is sequenced: run anywhere else its precondition is missing and it fails, rather than
 * passing over a phone that had nothing to restore.
 *
 * `no_install_apps` is put in effect first, because that is the phone this is about — a parent who
 * keeps the child from installing — and the restriction binds a device owner's own installs.
 */
@SequencedByAScript
@RunWith(AndroidJUnit4::class)
class SystemAppRestoreTest {

    private val clock = "com.google.android.deskclock"
    private val calendar = "com.google.android.calendar"

    private lateinit var context: Context
    private lateinit var users: UserManager
    private lateinit var restrictions: DpmRestrictionGateway

    @Before
    fun requireDeviceOwner() {
        context = InstrumentationRegistry.getInstrumentation().targetContext
        users = context.getSystemService(UserManager::class.java)
        val dpm = context.getSystemService(DevicePolicyManager::class.java)
        assertTrue("this test measures a phone managed by ${context.packageName}", dpm.isDeviceOwnerApp(context.packageName))
        restrictions = DpmRestrictionGateway(
            dpm,
            ComponentName(context.packageName, DpmRestrictionGateway.ADMIN_RECEIVER),
            users,
        )
    }

    @After
    fun clearTheInstallRestriction() {
        runCatching { restrictions.clear(EnforcementEngine.RESTRICTION_INSTALL_APPS) }
    }

    @Test
    fun aRemovedSystemAppComesBackAndABlockedOneStaysAway() {
        val policy = DeviceOwnerPolicy.of(context)
        assertNotNull("not device owner", policy)
        val apps = policy!!.apps

        val removed = apps.gateway.removedSystemApps()
        assertTrue(
            "precondition: $clock and $calendar removed for this user (system-apps.sh does that); " +
                "the gateway reports $removed",
            clock in removed && calendar in removed,
        )

        restrictions.add(EnforcementEngine.RESTRICTION_INSTALL_APPS)
        assertTrue("no_install_apps is not in effect", users.hasUserRestriction(UserManager.DISALLOW_INSTALL_APPS))

        val outcome = apps.apply(desiredSuspended = emptyList(), desiredHidden = listOf(calendar))

        assertTrue("$clock was not restored: $outcome", clock in outcome.restored)
        // The sync report's account of the search: both removed apps seen and launchable.
        assertTrue("the search is not described: $outcome", Regex("launchable=([2-9]|\\d{2,})").containsMatchIn(outcome.removedDiagnosis))
        assertNotNull(
            "$clock is restored and has no launcher entry for the child",
            context.packageManager.getLaunchIntentForPackage(clock),
        )
        assertTrue("$calendar is blocked and was put back anyway: $outcome", calendar in apps.gateway.removedSystemApps())
        assertTrue(
            "the restore window left no_install_apps lifted",
            users.hasUserRestriction(UserManager.DISALLOW_INSTALL_APPS),
        )
    }
}
