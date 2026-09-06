package io.github.helios57.familyguard.admin

import android.app.admin.DeviceAdminReceiver
import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.PersistableBundle
import android.util.Log
import androidx.core.content.IntentCompat
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import io.github.helios57.familyguard.recovery.AndroidRecoveryStore
import io.github.helios57.familyguard.recovery.RecoveryMode
import io.github.helios57.familyguard.sync.ConnectionService

/**
 * The device-admin component. Everything the framework calls back into lives here.
 *
 * There is no `onDisabled` that tries to resist being disabled: a device owner cannot be disabled
 * from Settings, and the paths that do remove it — a factory reset, or the parent deleting the
 * device from the console — are the ones this project deliberately keeps open (FR-2.3).
 */
class AdminReceiver : DeviceAdminReceiver() {

    /**
     * Provisioning finished. The device is now owned by this app, and nothing has been fetched from
     * the server yet.
     *
     * Applying the baseline here rather than waiting for the first sync closes the window where a
     * newly provisioned phone is a fully unmanaged one — a window that lasts as long as the child's
     * first walk out of Wi-Fi range.
     */
    override fun onProfileProvisioningComplete(context: Context, intent: Intent) {
        super.onProfileProvisioningComplete(context, intent)
        applyBaseline(context, "provisioning")

        // Both this callback and PolicyComplianceActivity start the connection, and which of them
        // fires — or whether both do — depends on the provisioning flow the OEM's setup wizard
        // takes. The service tolerates being started twice; a device that enrolled from neither
        // path because each assumed the other would is what the duplication buys off.
        ConnectionService.start(
            context,
            IntentCompat.getParcelableExtra(
                intent,
                DevicePolicyManager.EXTRA_PROVISIONING_ADMIN_EXTRAS_BUNDLE,
                PersistableBundle::class.java,
            ),
        )
    }

    override fun onEnabled(context: Context, intent: Intent) {
        super.onEnabled(context, intent)
        Log.i(TAG, "device admin enabled")
    }

    companion object {
        private const val TAG = "FamilyGuard/Admin"

        fun component(context: Context): ComponentName =
            ComponentName(context, AdminReceiver::class.java)

        /**
         * Applies the pre-sync baseline and logs what the platform actually did.
         *
         * The outcome is read back from the platform, so "not device owner" and "device owner, and
         * three restrictions silently did not take" are different log lines rather than the same
         * silence. Both are failures worth seeing in a bug report from a real phone; neither throws,
         * because the caller is a framework callback whose only alternative to continuing is to
         * crash the provisioning flow.
         */
        fun applyBaseline(context: Context, why: String) {
            val manager = DpmRestrictionGateway.hardeningManager(context)
            if (manager == null) {
                Log.w(TAG, "$why: not the device owner, baseline hardening was NOT applied")
                return
            }
            // A device a recovery code has released gets the floor with nothing in it (FR-12.6).
            // The baseline is "what is true before the server has been reached", and on a released
            // phone that is no longer true of anything: the parent typed a code precisely because
            // the server could not be reached, so re-adding six restrictions here would undo the
            // release on the first reboot and the sync that legitimately ends it is the thing that
            // is not coming. Reading the flag costs a keystore open; this is already the path that
            // makes device-policy calls, and both run inside the broadcast's window.
            val released = runCatching { RecoveryMode(AndroidRecoveryStore(context).mode).active() }
                .onFailure { Log.w(TAG, "$why: could not read recovery mode, assuming NOT released", it) }
                .getOrDefault(false)
            val outcome = if (released) manager.applyReleasedFloor() else manager.applyBaseline()
            if (released) {
                Log.i(TAG, "$why: this device is released by a recovery code, baseline NOT re-applied")
            }
            if (outcome.ok) {
                Log.i(TAG, "$why: baseline applied — $outcome")
            } else {
                Log.e(TAG, "$why: baseline INCOMPLETE — $outcome")
            }
        }
    }
}
