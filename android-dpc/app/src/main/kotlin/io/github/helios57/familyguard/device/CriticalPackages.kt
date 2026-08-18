package io.github.helios57.familyguard.device

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import android.provider.Telephony
import android.telecom.TelecomManager
import android.view.inputmethod.InputMethodManager

/**
 * The packages on *this* phone that must never be suspended, whatever the schedule says.
 *
 * FR-5.5. The engine ships a built-in list of AOSP package names, and on a Samsung or a Xiaomi that
 * list is wrong in the one direction that matters: the dialer is not `com.android.dialer`, so
 * bedtime suspends the child's ability to call for help and the parent finds out from the child.
 *
 * So the device reports its own, at enrollment, and the server unions them with its list. Union and
 * never narrow — a compromised or simply buggy device cannot use this to exempt a game.
 *
 * Everything here is read through a public API with no permission behind it, and every read is
 * allowed to fail: a phone with no telephony has no dialer, and reporting nothing for it is correct
 * rather than an error. What is never done is guessing.
 */
object CriticalPackages {

    /**
     * @return the dialer, launcher, SMS app, settings and enabled input methods, deduplicated.
     *
     * The IMEs are in the list because a suspended keyboard is a phone that cannot dial a number it
     * does not already know — a subtler version of the same failure as suspending the dialer.
     */
    fun onThisDevice(context: Context): List<String> {
        val found = LinkedHashSet<String>()

        // The dialer as the platform itself resolves it, which is what the emergency path uses.
        context.getSystemService(TelecomManager::class.java)?.defaultDialerPackage?.let { found += it }

        // The launcher. Suspending it does not merely hide an app — it leaves a phone whose home
        // button does nothing, which is indistinguishable from a brick to the person holding it.
        resolve(context, Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME))?.let { found += it }

        // Settings, so that Wi-Fi, aeroplane mode and — the point of FR-2.3 — the reset menu stay
        // reachable while the phone is locked down.
        resolve(context, Intent(Settings.ACTION_SETTINGS))?.let { found += it }

        Telephony.Sms.getDefaultSmsPackage(context)?.let { found += it }

        context.getSystemService(InputMethodManager::class.java)
            ?.enabledInputMethodList
            ?.forEach { found += it.serviceInfo.packageName }

        // The framework's own resolver answers with this when nothing is set as default; it is not a
        // package the child uses and suspending it was never possible anyway.
        found -= "android"
        return found.filter { it.isNotBlank() }
    }

    /**
     * @return the package that would handle [intent], or null when nothing would.
     *
     * The two branches are the same call: `resolveActivity(Intent, Int)` is deprecated from API 33
     * and the replacement does not exist below it, so there is no single expression that compiles
     * without a warning across this app's whole supported range (minSdk 29). The suppression is on
     * the legacy branch alone, which is the only place it is true.
     */
    private fun resolve(context: Context, intent: Intent): String? {
        val pm = context.packageManager
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.resolveActivity(
                intent,
                PackageManager.ResolveInfoFlags.of(PackageManager.MATCH_DEFAULT_ONLY.toLong()),
            )
        } else {
            @Suppress("DEPRECATION")
            pm.resolveActivity(intent, PackageManager.MATCH_DEFAULT_ONLY)
        }
        return info?.activityInfo?.packageName
    }
}
