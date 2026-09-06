package io.github.helios57.familyguard.enroll

import android.content.Context
import android.os.Build
import io.github.helios57.familyguard.device.CriticalPackages

/**
 * What this hardware calls itself, read from the platform.
 *
 * A top-level function rather than a method on `ConnectionService`, because two callers now send
 * these facts to the same endpoint: the first enrollment, and a re-link from the recovery screen
 * (FR-1.8). Two copies would drift, and the way they would drift is the second one reporting no
 * critical packages — which is the field that stops bedtime suspending an OEM dialer, and whose
 * absence looks like nothing at all until a child cannot call for help.
 */
fun androidDeviceFacts(context: Context): DeviceFacts = DeviceFacts(
    model = "${Build.MANUFACTURER} ${Build.MODEL}".trim(),
    osVersion = "Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})",
    criticalPackages = CriticalPackages.onThisDevice(context),
)
