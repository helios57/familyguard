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
)

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
) : InstalledAppReader {

    private var lastReason: String = ""

    override fun installed(): List<InstalledApp>? {
        val pm = context.packageManager
        val infos = runCatching { applications(pm) }.getOrElse {
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
    private fun applications(pm: PackageManager): List<ApplicationInfo> =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getInstalledApplications(PackageManager.ApplicationInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            pm.getInstalledApplications(0)
        }
}
