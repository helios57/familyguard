package io.github.helios57.familyguard.admin

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.github.helios57.familyguard.sync.ConnectionService

/**
 * Re-applies the pre-sync baseline after a reboot.
 *
 * User restrictions set by a device owner do survive a reboot, so on a healthy device this is a
 * no-op — [io.github.helios57.familyguard.policy.RestrictionPlanner] computes an empty plan and nothing is
 * called. It is here for the device that is *not* healthy: one whose restrictions were dropped by an
 * OEM update or a partial provisioning, which otherwise stays unmanaged until the next successful
 * sync, and stays unmanaged forever if that sync never comes.
 *
 * The baseline, not the last known policy. Restoring a cached policy at boot would mean re-suspending
 * apps and re-blocking domains from a snapshot of unknown age; the baseline is the subset that is
 * true regardless of what the parent has changed since (FR-9).
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        AdminReceiver.applyBaseline(context, "boot")
        // With no extras: a device that reaches a reboot has either enrolled — in which case the
        // credential is stored and the extras are irrelevant — or never did, in which case the
        // enrollment token in the QR was single-use and is long spent.
        ConnectionService.start(context, null)
    }
}
