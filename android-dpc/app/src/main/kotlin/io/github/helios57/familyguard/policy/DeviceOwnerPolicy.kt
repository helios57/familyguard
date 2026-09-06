package io.github.helios57.familyguard.policy

import android.app.KeyguardManager
import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import io.github.helios57.familyguard.device.CriticalPackages
import io.github.helios57.familyguard.enforce.EnforcementEngine

/**
 * Every manager that needs device ownership, built once and handed to the appliers.
 *
 * Built together rather than one at a time because they share the same precondition: without device
 * ownership none of them can do anything, and four separate null checks scattered through the
 * service is four chances to write the branch that silently carries on.
 */
class DeviceOwnerPolicy(
    val hardening: HardeningManager,
    val apps: AppSuspensionManager,
    val chrome: ChromePolicyManager,
    val dns: DnsPolicyManager,
    val lock: LockManager,
) {
    companion object {
        /**
         * @return the managers, or null when this app is not the device owner.
         *
         * Null rather than managers whose every call throws: what to do about not being device owner
         * differs by caller — at provisioning it is a failure, at boot on a phone that was never
         * enrolled it is expected — and only the caller knows which it is in.
         */
        fun of(context: Context): DeviceOwnerPolicy? {
            // Delegated rather than re-derived: `hardeningManager` already decides what "device
            // owner" means, and two copies of that decision is one that can drift into a build where
            // the restrictions path is disabled and the app path is not.
            val hardening = DpmRestrictionGateway.hardeningManager(context) ?: return null
            val dpm = context.getSystemService(DevicePolicyManager::class.java) ?: return null
            val admin = ComponentName(context.packageName, DpmRestrictionGateway.ADMIN_RECEIVER)

            // The union the engine documents: its own built-in list, plus what this hardware
            // actually resolves as dialer/launcher/SMS/settings/IME, plus this app. Read here rather
            // than trusted from the server, because the server's copy is only as fresh as the last
            // enrollment and this list is what stands between a bedtime and a child who cannot dial.
            val protectedPackages = buildSet {
                addAll(EnforcementEngine.DEFAULT_CRITICAL_PACKAGES)
                addAll(CriticalPackages.onThisDevice(context))
                add(context.packageName)
            }

            return DeviceOwnerPolicy(
                hardening = hardening,
                apps = AppSuspensionManager(
                    gateway = DpmAppGateway(dpm, admin, context.packageManager),
                    protectedPackages = protectedPackages,
                    ownPackage = context.packageName,
                ),
                chrome = ChromePolicyManager(DpmManagedConfigGateway(dpm, admin)),
                dns = DnsPolicyManager(DpmDnsGateway(dpm, admin)),
                lock = LockManager(DpmLockGateway(dpm, context.getSystemService(KeyguardManager::class.java))),
            )
        }
    }
}

/**
 * [AppGateway] over the real platform. Thin on purpose: every decision is in
 * [AppSuspensionPlanner] and [AppSuspensionManager], both covered by JVM tests.
 */
class DpmAppGateway(
    private val dpm: DevicePolicyManager,
    private val admin: ComponentName,
    private val packages: PackageManager,
) : AppGateway {

    /**
     * `getInstalledApplications` and not a cached list: an app installed since the last sync is
     * exactly the one FR-5.4 is about.
     *
     * A device owner is exempt from package-visibility filtering, which is why this is not wrapped
     * in a `QUERY_ALL_PACKAGES` request — and because "is exempt" is a claim about a platform
     * behaviour rather than something this code can enforce, [AppSuspensionManager] checks that this
     * app's own package is in the result before it believes any of it.
     *
     * Two reads, unioned by [InstalledPackages]: the narrow one does not return a package this DPC
     * has hidden, and a hidden package that cannot be enumerated can never be revealed. The rule
     * lives in that object so it can be tested without a phone; what is untestable here — that the
     * narrow read omits hidden packages — is a platform behaviour, and the union is correct either
     * way, because on a platform that does not omit them the second read finds nothing new.
     */
    override fun installed(): Set<String> = InstalledPackages.union(
        present = applications(0).map { it.packageName },
        known = applications(PackageManager.MATCH_UNINSTALLED_PACKAGES.toLong()).map { it.packageName },
        isHidden = { runCatching { dpm.isApplicationHidden(admin, it) }.getOrDefault(false) },
    )

    /**
     * The same call twice: the flags overload is deprecated from API 33 and its replacement does not
     * exist below it, so nothing compiles warning-free across minSdk 29 to current in one expression.
     */
    private fun applications(flags: Long): List<ApplicationInfo> =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            packages.getInstalledApplications(PackageManager.ApplicationInfoFlags.of(flags))
        } else {
            @Suppress("DEPRECATION")
            packages.getInstalledApplications(flags.toInt())
        }

    /**
     * `isPackageSuspended` throws for a package that is not installed, which is a normal state here
     * (a policy naming an app the child never had), so it is answered as "not suspended" rather than
     * propagated. Every other exception is propagated: a `SecurityException` means this app is not
     * the owner any more, and swallowing it would report a phone that suspends nothing as clean.
     */
    override fun suspended(): Set<String> = installed().filterTo(mutableSetOf()) {
        try {
            dpm.isPackageSuspended(admin, it)
        } catch (_: PackageManager.NameNotFoundException) {
            false
        }
    }

    override fun hidden(): Set<String> =
        installed().filterTo(mutableSetOf()) { dpm.isApplicationHidden(admin, it) }

    override fun setSuspended(packages: List<String>, suspended: Boolean): Map<String, String> {
        val refused = dpm.setPackagesSuspended(admin, packages.toTypedArray(), suspended)
        // The platform returns the names it did not act on and says nothing about why. "the platform
        // did not act on it" is the honest wording — a package that is not installed and a package
        // the platform protects arrive here identically.
        return refused.associateWith { "the platform did not act on it" }
    }

    override fun setHidden(pkg: String, hidden: Boolean): Boolean =
        dpm.setApplicationHidden(admin, pkg, hidden)
}

/** [ManagedConfigGateway] over `DevicePolicyManager`'s application-restrictions bundle. */
class DpmManagedConfigGateway(
    private val dpm: DevicePolicyManager,
    private val admin: ComponentName,
) : ManagedConfigGateway {

    override fun current(pkg: String): Map<String, Any> {
        val bundle = dpm.getApplicationRestrictions(admin, pkg)
        return bundle.keySet().mapNotNull { key ->
            @Suppress("DEPRECATION") // the typed overload is API 33; minSdk here is 29
            val value = bundle.get(key)
            value?.let { key to it }
        }.toMap()
    }

    /**
     * The bundle is built from scratch every time, never read-modify-written. See
     * [ChromePolicyManager] — `setApplicationRestrictions` replaces rather than merges, and a
     * merging writer is how two components silently drop each other's policy.
     */
    override fun set(pkg: String, config: Map<String, Any>) {
        val bundle = Bundle()
        for ((key, value) in config) {
            when (value) {
                is Boolean -> bundle.putBoolean(key, value)
                is Int -> bundle.putInt(key, value)
                is String -> bundle.putString(key, value)
                is List<*> -> bundle.putStringArray(key, value.map { it.toString() }.toTypedArray())
                // Reached only by a future key of a type this does not handle. Throwing is the point:
                // a silently dropped key is a policy that reads back short and looks like a platform
                // problem.
                else -> throw IllegalArgumentException("$key: unsupported managed-config type ${value.javaClass}")
            }
        }
        dpm.setApplicationRestrictions(admin, pkg, bundle)
    }
}

/**
 * [LockGateway] over `lockNow` and the keyguard.
 *
 * Two services, because the platform splits them: `DevicePolicyManager` is the only thing that can
 * raise the keyguard, and `KeyguardManager` is the only thing that can say whether raising it means
 * anything — [LockManager] explains why the second question is the one that matters.
 *
 * A null [keyguard] is treated as "cannot be read" rather than as a failure, so a device that does
 * not offer the service still gets locked and the console is told the guarantee was not checked.
 */
class DpmLockGateway(
    private val dpm: DevicePolicyManager,
    private val keyguard: KeyguardManager?,
) : LockGateway {

    override fun lockNow() = dpm.lockNow()

    override fun deviceSecure(): Boolean =
        keyguard?.isDeviceSecure ?: throw IllegalStateException("this device has no keyguard service")

    override fun deviceLocked(): Boolean =
        keyguard?.isDeviceLocked ?: throw IllegalStateException("this device has no keyguard service")
}

/** [DnsGateway] over the global private-DNS API, which is API 29 and up — hence this app's minSdk. */
class DpmDnsGateway(
    private val dpm: DevicePolicyManager,
    private val admin: ComponentName,
) : DnsGateway {

    override fun mode(): PrivateDnsMode = when (dpm.getGlobalPrivateDnsMode(admin)) {
        DevicePolicyManager.PRIVATE_DNS_MODE_OFF -> PrivateDnsMode.OFF
        DevicePolicyManager.PRIVATE_DNS_MODE_OPPORTUNISTIC -> PrivateDnsMode.OPPORTUNISTIC
        DevicePolicyManager.PRIVATE_DNS_MODE_PROVIDER_HOSTNAME -> PrivateDnsMode.HOSTNAME
        else -> PrivateDnsMode.UNKNOWN
    }

    override fun host(): String? = dpm.getGlobalPrivateDnsHost(admin)

    override fun setSpecifiedHost(host: String): PrivateDnsResult =
        translate(dpm.setGlobalPrivateDnsModeSpecifiedHost(admin, host))

    override fun setOpportunistic(): PrivateDnsResult =
        translate(dpm.setGlobalPrivateDnsModeOpportunistic(admin))

    private fun translate(code: Int): PrivateDnsResult = when (code) {
        DevicePolicyManager.PRIVATE_DNS_SET_NO_ERROR -> PrivateDnsResult.OK
        DevicePolicyManager.PRIVATE_DNS_SET_ERROR_HOST_NOT_SERVING -> PrivateDnsResult.HOST_NOT_SERVING
        else -> PrivateDnsResult.FAILED
    }
}
