package io.github.helios57.familyguard.device

import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Build

/** One app as the console will show it: what it is called, and whether the child could remove it. */
data class InstalledApp(
    val packageName: String,
    val label: String,
    val systemApp: Boolean,
    /** Hidden by this DPC — the app is on the phone and the child cannot see or launch it. */
    val hidden: Boolean = false,
    /** Suspended by this DPC — visible, greyed out, and it will not start. */
    val suspended: Boolean = false,
)

/**
 * What this device currently restrains, so the inventory can say so per app (FR-18.6).
 *
 * Separate from the inventory read because it needs device ownership and the inventory does not: a
 * phone that is not the owner still reports its apps, and reports every one of them as unrestrained,
 * which is the truth.
 */
interface AppRestraint {
    fun hidden(): Set<String>

    fun suspended(): Set<String>
}

/**
 * The installed-app inventory (FR-5.1).
 *
 * `null` means **not measured**, not "no apps". The two are separated here for the same reason as in
 * the usage reader: an inventory that arrives empty does not read as a broken device, it reads as a
 * child with a bare phone — and every app rule a parent then writes is against a list the console
 * does not have.
 */
interface InstalledAppReader {
    fun installed(): List<InstalledApp>?

    fun unavailableReason(): String
}

/**
 * Reads the inventory from `PackageManager`, and refuses to believe a filtered answer.
 *
 * Android 11 filters `getInstalledApplications` for an app without `QUERY_ALL_PACKAGES` down to
 * roughly what that app already interacts with. This app holds the permission, but a build that
 * dropped it, a ROM that ignores it, or a work-profile boundary would all produce a short list with
 * no error anywhere — the console would show four apps, the parent would block those four, and the
 * rest of the phone would be unmanaged.
 *
 * The check is the same one [io.github.helios57.familyguard.policy.AppSuspensionManager] makes: an inventory
 * that does not contain the app doing the reading is not an inventory of this device.
 */
class PlatformInstalledAppReader(
    private val context: Context,
    private val ownPackage: String = context.packageName,
    /**
     * Null when this app is not the device owner. Two jobs: it says which apps are restrained, and
     * it is the only way a *hidden* app stays in the inventory at all — hiding clears the
     * installed-for-this-user flag, so the plain read drops it. Without this the console would stop
     * listing the very apps the family blocked and show them as "not installed here", which is the
     * one answer a parent asking "is the bloatware gone?" must never be given.
     */
    private val restraint: AppRestraint? = null,
) : InstalledAppReader {

    private var lastReason: String = ""

    override fun installed(): List<InstalledApp>? {
        val pm = context.packageManager
        val hidden = runCatching { restraint?.hidden() }.getOrElse {
            lastReason = "the hidden-app read failed, so the inventory would under-report: ${it.message}"
            return null
        }.orEmpty()
        val suspended = runCatching { restraint?.suspended() }.getOrElse {
            lastReason = "the suspended-app read failed, so the inventory would under-report: ${it.message}"
            return null
        }.orEmpty()
        val infos = runCatching {
            val present = applications(pm, 0)
            if (hidden.isEmpty()) present else {
                // Only when something is hidden, so a phone with nothing blocked pays nothing. The
                // wide read is filtered by what DPM says is hidden rather than taken whole: it also
                // returns packages that are genuinely gone with only their data retained, and the
                // console would list them as apps the child has.
                val names = present.mapTo(mutableSetOf()) { it.packageName }
                present + applications(pm, PackageManager.MATCH_UNINSTALLED_PACKAGES.toLong())
                    .filter { it.packageName !in names && it.packageName in hidden }
            }
        }.getOrElse {
            lastReason = "the package manager refused the inventory read: ${it.message}"
            return null
        }
        val apps = infos.map {
            InstalledApp(
                packageName = it.packageName,
                // The label is what a parent recognises; the package name is what the rules are
                // written against. Both are sent, and a label that cannot be resolved falls back to
                // the package name rather than to an empty string a console would render as a blank
                // row.
                label = runCatching { pm.getApplicationLabel(it).toString() }
                    .getOrDefault(it.packageName),
                systemApp = it.flags and
                    (ApplicationInfo.FLAG_SYSTEM or ApplicationInfo.FLAG_UPDATED_SYSTEM_APP) != 0,
                hidden = it.packageName in hidden,
                suspended = it.packageName in suspended,
            )
        }.sortedBy { it.packageName }

        if (apps.none { it.packageName == ownPackage }) {
            lastReason = "the installed-app read does not contain this app (${apps.size} entries), " +
                "so package visibility is filtering it and the list is not this device's"
            return null
        }
        lastReason = ""
        return apps
    }

    override fun unavailableReason(): String =
        lastReason.ifEmpty { "the inventory has not been read yet" }

    /**
     * The same call twice: the flags overload is deprecated from API 33 and its replacement does not
     * exist below it, so nothing compiles warning-free across minSdk 29 to current in one expression.
     */
    private fun applications(pm: PackageManager, flags: Long): List<ApplicationInfo> =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getInstalledApplications(PackageManager.ApplicationInfoFlags.of(flags))
        } else {
            @Suppress("DEPRECATION")
            pm.getInstalledApplications(flags.toInt())
        }
}
