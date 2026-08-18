package io.github.helios57.familyguard.policy

import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.os.Build

/**
 * [ClockGateway] over the real platform, and the one place in this app that has to care which
 * Android version it is running on.
 *
 * The API moved at 30. `setAutoTimeRequired`/`getAutoTimeRequired` are the API 21 pair, deprecated
 * at 30; `setAutoTimeEnabled`/`getAutoTimeEnabled` replace them and do not exist before it. minSdk
 * is 29 (NFR-13), so both halves are reachable and the split is real rather than defensive.
 *
 * The split is deliberately *not* "call the deprecated one everywhere, it still works". It does
 * still work at 30+, but it means something different there: `setAutoTimeRequired(true)` forbids
 * the user from turning automatic time off, while leaving it off if it already was. That is a
 * lock on the wrong position, and it is precisely the failure FR-2.2 exists to prevent — so on 30+
 * the value is *enabled*, and the restriction that stops it being changed is
 * `DISALLOW_CONFIG_DATE_TIME`, which the baseline already carries.
 *
 * As thin as [DpmRestrictionGateway], for the same reason: every judgement is in
 * [ClockPolicyManager], which is covered by JVM tests. What is left here is a version check.
 */
class DpmClockGateway(
    private val dpm: DevicePolicyManager,
    private val admin: ComponentName,
) : ClockGateway {

    override fun autoTimeEnabled(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            dpm.getAutoTimeEnabled(admin)
        } else {
            @Suppress("DEPRECATION")
            dpm.autoTimeRequired
        }

    override fun enableAutoTime() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            dpm.setAutoTimeEnabled(admin, true)
        } else {
            @Suppress("DEPRECATION")
            dpm.setAutoTimeRequired(admin, true)
        }
    }
}
