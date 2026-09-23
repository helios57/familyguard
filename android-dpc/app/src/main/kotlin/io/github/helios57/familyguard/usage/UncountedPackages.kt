package io.github.helios57.familyguard.usage

import android.content.Context
import io.github.helios57.familyguard.device.CriticalPackages
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.enforce.TodayReport

/**
 * Foreground time on this phone that is not use (FR-3.8): what the server said, plus what this
 * phone knows for itself — its home screen now, System UI, and this app. The server's list comes
 * from this phone's own reports, so the union only matters while the two are catching up (a
 * launcher switched since the last heartbeat, or a phone that has never been told).
 */
object UncountedPackages {
    fun on(context: Context, input: Input?): Set<String> = buildSet {
        input?.uncountedPackages?.let { addAll(it) }
        addAll(CriticalPackages.homeScreen(context))
        add(TodayReport.SYSTEM_UI)
        add(context.packageName)
    }
}
