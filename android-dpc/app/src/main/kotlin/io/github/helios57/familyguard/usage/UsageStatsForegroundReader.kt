package io.github.helios57.familyguard.usage

import android.app.AppOpsManager
import android.app.usage.UsageEvents
import android.app.usage.UsageStatsManager
import android.content.Context
import android.os.Process
import android.util.Log

/**
 * The platform's own record of what was in the foreground, read through `UsageStatsManager`.
 *
 * **This needs an access grant that being Device Owner does not give.** `PACKAGE_USAGE_STATS` is an
 * appop, not a runtime permission: `setPermissionGrantState` cannot grant it, and there is no
 * device-owner API that can. It is turned on once per device, either from Settings → Apps → Special
 * app access → Usage access, or with `adb shell appops set io.github.helios57.familyguard GET_USAGE_STATS
 * allow`. DEPLOYMENT.md carries it as a provisioning step.
 *
 * The whole reason [spans] answers `null` rather than an empty list when the grant is missing is
 * that this is the single most dangerous silent failure in the product. Without the grant every
 * query returns nothing, every package reads zero minutes, the daily limit is never reached, and the
 * console shows a child who spent the day off their phone. A parent has no way to tell that from the
 * real thing. Not-measured travels all the way to the status screen instead.
 */
class UsageStatsForegroundReader(private val context: Context) : ForegroundReader {

    override fun spans(fromMillis: Long, toMillis: Long): List<ForegroundSpan>? {
        if (!hasUsageAccess()) return null
        val manager = context.getSystemService(UsageStatsManager::class.java) ?: return null
        val events = try {
            collect(manager.queryEvents(fromMillis, toMillis))
        } catch (e: RuntimeException) {
            // A throwing platform is not zero usage either. Reported as not-measured for the same
            // reason as a missing grant.
            Log.w(TAG, "queryEvents failed: ${e.message}")
            return null
        }
        return SpanFolder.fold(events, toMillis)
    }

    override fun unavailableReason(): String =
        if (hasUsageAccess()) {
            "usage access is granted; the platform returned nothing"
        } else {
            "usage access (PACKAGE_USAGE_STATS) is not granted, so screen time cannot be measured " +
                "and no quota can be enforced"
        }

    /**
     * Whether the appop is allowed for *this* uid and package.
     *
     * **`checkOpNoThrow`, and the platform reversed itself on which of the two names that is.** In
     * API 29 `checkOpNoThrow(String, int, String)` was deprecated in favour of
     * `unsafeCheckOpNoThrow`, so that is what this called. In API 37 the deprecation moved back the
     * other way: measured against the platform stubs, `checkOpNoThrow(String, int, String)` carries
     * no `Deprecated` attribute in `android-37.1/android.jar` and `unsafeCheckOpNoThrow` does — the
     * exact opposite of `android-35/android.jar`, where the same two-line probe reports the reverse.
     * Nothing about the semantics changed: both answer a mode instead of throwing, and both are
     * being asked about this process's own uid and package.
     *
     * This is what `allWarningsAsErrors` is for. The rename is invisible at runtime and would have
     * ridden along unnoticed for as long as the old name kept working.
     *
     * `runCatching` stays regardless of which name is current. A `SecurityException` here would mean
     * the caller is not the package it is asking about, which cannot happen — and if the platform
     * ever makes it happen, not-measured is the honest answer, not a crashed usage poll.
     */
    private fun hasUsageAccess(): Boolean {
        val appOps = context.getSystemService(AppOpsManager::class.java) ?: return false
        val mode = runCatching {
            appOps.checkOpNoThrow(
                AppOpsManager.OPSTR_GET_USAGE_STATS,
                Process.myUid(),
                context.packageName,
            )
        }.getOrElse {
            Log.w(TAG, "could not read the usage-access appop: ${it.message}")
            return false
        }
        return mode == AppOpsManager.MODE_ALLOWED
    }

    private fun collect(events: UsageEvents): List<ForegroundEvent> {
        val out = mutableListOf<ForegroundEvent>()
        val event = UsageEvents.Event()
        while (events.hasNextEvent()) {
            events.getNextEvent(event)
            val kind = when (event.eventType) {
                UsageEvents.Event.ACTIVITY_RESUMED -> ForegroundEventKind.RESUMED
                // STOPPED as well as PAUSED: an app killed in the background emits the first without
                // the second, and a session left open by that would run to the end of the window.
                UsageEvents.Event.ACTIVITY_PAUSED,
                UsageEvents.Event.ACTIVITY_STOPPED -> ForegroundEventKind.PAUSED
                UsageEvents.Event.SCREEN_NON_INTERACTIVE,
                UsageEvents.Event.DEVICE_SHUTDOWN -> ForegroundEventKind.SCREEN_OFF
                else -> continue
            }
            out += ForegroundEvent(kind, event.packageName.orEmpty(), event.timeStamp)
        }
        return out
    }

    private companion object {
        const val TAG = "FamilyGuard/Usage"
    }
}
