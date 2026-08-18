package io.github.helios57.familyguard.policy

import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.os.UserManager

/**
 * [RestrictionGateway] over the real platform. Deliberately the thinnest thing that can work: every
 * decision lives in [RestrictionPlanner] and [HardeningManager], both of which are covered by JVM
 * tests, so what is left here has no branch to get wrong.
 */
class DpmRestrictionGateway(
    private val dpm: DevicePolicyManager,
    private val admin: ComponentName,
    private val users: UserManager,
) : RestrictionGateway {

    /**
     * `getUserRestrictions()` returns a bundle that can carry a key with the value `false` — an
     * "explicitly not restricted" that reads as present to anything checking key membership. Only
     * the ones that are actually true are in effect.
     */
    override fun current(): Set<String> {
        val bundle = users.userRestrictions
        return bundle.keySet().filterTo(mutableSetOf()) { bundle.getBoolean(it) }
    }

    override fun add(key: String) = dpm.addUserRestriction(admin, key)

    override fun clear(key: String) = dpm.clearUserRestriction(admin, key)

    companion object {
        /**
         * @return a manager bound to this device, or `null` when the app is not the device owner.
         *
         * Null rather than a manager whose every call throws: the caller has to decide what to do
         * about not being device owner, and that decision is different at provisioning time (a
         * genuine failure) from at boot time on a device that was never enrolled (expected).
         */
        fun hardeningManager(context: Context): HardeningManager? {
            val dpm = context.getSystemService(DevicePolicyManager::class.java) ?: return null
            val users = context.getSystemService(UserManager::class.java) ?: return null
            if (!dpm.isDeviceOwnerApp(context.packageName)) return null
            val admin = ComponentName(context.packageName, ADMIN_RECEIVER)
            return HardeningManager(
                DpmRestrictionGateway(dpm, admin, users),
                ClockPolicyManager(DpmClockGateway(dpm, admin)),
            )
        }

        /**
         * Named as a string rather than as `AdminReceiver::class.java` to keep this package free of
         * a dependency on `admin`, which depends on it. The manifest is the other end of the same
         * name, and `AdminReceiverNameTest` asserts the two agree — a typo here would produce a
         * gateway whose every call throws `SecurityException` on a device that is correctly
         * provisioned, which is the least diagnosable failure in this file.
         */
        const val ADMIN_RECEIVER = "io.github.helios57.familyguard.admin.AdminReceiver"
    }
}
