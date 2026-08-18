package io.github.helios57.familyguard.provisioning

import android.app.Activity
import android.app.admin.DevicePolicyManager
import android.os.Bundle
import android.os.PersistableBundle
import android.util.Log
import androidx.core.content.IntentCompat
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import io.github.helios57.familyguard.sync.ConnectionService

/**
 * The last step of provisioning: the platform hands control here and asks whether the device now
 * complies with what this admin requires.
 *
 * The answer is not "yes" by default. A `RESULT_OK` from an activity that checked nothing is how a
 * phone ends up enrolled in the console and unmanaged in the child's pocket, and the parent's only
 * signal would be a device that never enforces a bedtime.
 *
 * Two states refuse, and both leave a phone the parent can still use:
 *
 * - **Not the device owner.** Provisioning reached this point without granting ownership, so nothing
 *   this app enforces can work. Completing would produce a device that reports itself managed.
 * - **A forbidden restriction is in effect.** Something set `no_factory_reset`, so the phone can no
 *   longer be wiped from recovery — the escape hatch this project keeps open on purpose (FR-2.3,
 *   NFR-6). Refusing here aborts provisioning while the device is still recoverable.
 *
 * A restriction that merely failed to apply does *not* refuse. A phone missing `no_debugging` on an
 * OEM build that rejects it is far better than an unmanaged one, and the outcome is logged in full
 * so the gap is visible rather than assumed away.
 */
class PolicyComplianceActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // The enrollment token and control-plane URL arrive in EXTRA_PROVISIONING_ADMIN_EXTRAS_BUNDLE
        // and are handed to the connection service below. They are deliberately read *after* the
        // baseline is applied: the baseline must hold whether or not enrollment then succeeds,
        // because a device that provisioned and failed to enroll is exactly the one that should not
        // be wide open.
        val manager = DpmRestrictionGateway.hardeningManager(this)
        if (manager == null) {
            Log.e(TAG, "provisioning completed without device ownership; refusing to report compliance")
            setResult(RESULT_CANCELED)
            finish()
            return
        }

        val outcome = manager.applyBaseline()
        if (outcome.stillForbidden.isNotEmpty()) {
            Log.e(TAG, "refusing to complete provisioning: the device is not wipeable — $outcome")
            setResult(RESULT_CANCELED)
            finish()
            return
        }
        if (outcome.ok) {
            Log.i(TAG, "baseline applied at provisioning — $outcome")
        } else {
            Log.w(TAG, "baseline applied with gaps at provisioning — $outcome")
        }

        // Enrollment runs in the connection service rather than here. This activity is finishing:
        // an enrollment started from it would be racing its own process being torn down, and the
        // first attempt on a freshly provisioned phone is the one most likely to need a retry —
        // Wi-Fi is associated and DNS has not settled. The service is the component that can wait.
        ConnectionService.start(this, adminExtras())

        setResult(RESULT_OK)
        finish()
    }

    /**
     * The bundle the provisioning QR carried, or null when there was none.
     *
     * Null is not a failure to report here. A device provisioned by hand — `adb shell dpm
     * set-device-owner` on a test phone — reaches this with no extras and is a perfectly good
     * device owner; it simply has nothing to enroll with, and the service says so once.
     */
    private fun adminExtras(): PersistableBundle? = IntentCompat.getParcelableExtra(
        intent,
        DevicePolicyManager.EXTRA_PROVISIONING_ADMIN_EXTRAS_BUNDLE,
        PersistableBundle::class.java,
    )

    private companion object {
        const val TAG = "FamilyGuard/Compliance"
    }
}
