package io.github.helios57.familyguard.provisioning

import android.app.Activity
import android.app.admin.DevicePolicyManager
import android.content.Intent
import android.os.Bundle

/**
 * Answers the setup wizard's question of what kind of enrollment this is (FR-1.5).
 *
 * Fully managed device, and nothing else. A work profile cannot set global private DNS (FR-6.1) and
 * cannot suspend the child's personal apps (FR-4), so offering it would produce a phone that
 * completes enrollment, appears in the console, and enforces almost none of what the parent
 * configured — the most misleading state this app could reach.
 *
 * No UI: there is no choice to present. The activity exists because the framework requires an
 * answer, so it answers and finishes before anything is drawn.
 */
class GetProvisioningModeActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setResult(
            RESULT_OK,
            Intent().putExtra(
                DevicePolicyManager.EXTRA_PROVISIONING_MODE,
                DevicePolicyManager.PROVISIONING_MODE_FULLY_MANAGED_DEVICE,
            ),
        )
        finish()
    }
}
