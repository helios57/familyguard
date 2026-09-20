package io.github.helios57.familyguard.policy

/**
 * The two calls always-on VPN needs, named so the decision can be tested without a phone.
 *
 * [setPackage] takes null to clear it. `lockdown` is never anything but false here and is still a
 * parameter, because a gateway that cannot express it is a gateway where the dangerous value is
 * invisible — see [AlwaysOnVpnManager].
 */
interface AlwaysOnVpnGateway {
    fun packageName(): String?
    fun isLockdownEnabled(): Boolean
    fun setPackage(packageName: String?, lockdown: Boolean)
}

data class AlwaysOnVpnOutcome(val summary: String, val failure: String? = null) {
    val ok: Boolean get() = failure == null

    override fun toString(): String = if (ok) summary else "$summary FAILED=$failure"
}

/**
 * Makes the ad filter's tunnel the device's always-on VPN — **never in lockdown mode** (FR-6.7).
 *
 * Always-on is what removes the consent dialog. A `VpnService` normally needs a user to tap
 * "OK, I trust this app", which a child's phone cannot rely on and a parent cannot reach; a device
 * owner naming its own package here is granted the consent implicitly, and the platform brings the
 * service back after a reboot or a crash without anything on the phone having to remember to.
 *
 * ### Lockdown is the switch that bricks the phone, and it is false everywhere
 *
 * With lockdown on, the platform drops every packet whenever the tunnel is not up — that is the
 * whole point of it, and it is correct for a corporate laptop. On a child's phone it means that a
 * bug in this app, a service the platform declined to start, a crash loop, or simply an APK that
 * was replaced badly, all become a phone with no internet at all: no browser, no messaging, and no
 * sync — so **the one thing that could switch the filter back off cannot reach the device.** The
 * only remaining exit is a factory reset, which is explicitly not something this project is willing
 * to require of a parent.
 *
 * Without lockdown, every one of those failures is a phone that simply stops filtering. The cost of
 * the safe direction is an advertisement; the cost of the other is a phone nobody can fix.
 *
 * So the constant is written once, here, with the reason attached, and this class never takes it as
 * a parameter. [AlwaysOnVpnGateway.setPackage] still has the argument so that a reader of the
 * gateway can see which value is being passed, rather than finding a call that quietly omits it.
 *
 * ### `addDisallowedApplication` and lockdown are the pair that actually brick a device
 *
 * They are safe apart and dangerous together: under lockdown, a disallowed app is an app with no
 * network at all, forever. This class keeps lockdown off, and [io.github.helios57.familyguard.filter.AdFilterVpnService]
 * exempts only its own package — which is what keeps the sync that can switch this off outside the
 * tunnel it might be the reason to switch off.
 */
class AlwaysOnVpnManager(
    private val gateway: AlwaysOnVpnGateway,
    private val ownPackage: String,
) {

    /**
     * @param wanted true to make this app the always-on VPN, false to clear it.
     *
     * Clearing is unconditional when [wanted] is false, including when some *other* package holds
     * it: nothing else should ever be the always-on VPN on a device this app owns, and a foreign
     * package there is a state a parent cannot see and cannot undo.
     */
    fun apply(wanted: Boolean): AlwaysOnVpnOutcome {
        val current = try {
            gateway.packageName()
        } catch (e: RuntimeException) {
            return AlwaysOnVpnOutcome("always-on", failure = e.message ?: e.javaClass.simpleName)
        }
        val lockdown = try {
            gateway.isLockdownEnabled()
        } catch (e: RuntimeException) {
            // Unknown lockdown state is treated as "must be re-set", because the one value that
            // matters here is the one that cannot be read.
            true
        }

        val target = if (wanted) ownPackage else null
        // Re-set even when the package already matches, if lockdown is on. An earlier build, an
        // OEM default or a hand-run `dpm` command could have left it on, and this is the only place
        // that turns it off.
        if (current == target && !(wanted && lockdown)) {
            return AlwaysOnVpnOutcome(
                if (wanted) "always-on=$ownPackage lockdown=false (unchanged)" else "always-on=none (unchanged)",
            )
        }

        try {
            gateway.setPackage(target, LOCKDOWN)
        } catch (e: RuntimeException) {
            // UnsupportedOperationException is what the platform throws when the package does not
            // declare a VpnService, or when always-on is not supported. Both are "the filter does
            // not run", never "the phone loses its network".
            return AlwaysOnVpnOutcome(
                if (wanted) "always-on=$ownPackage" else "always-on=none",
                failure = e.message ?: e.javaClass.simpleName,
            )
        }

        // The authority is the read-back, not the absence of an exception: a device that accepts
        // the call and comes back holding nothing is a phone that is not filtering while the
        // console shows the filter on.
        val after = try {
            gateway.packageName()
        } catch (e: RuntimeException) {
            return AlwaysOnVpnOutcome("always-on", failure = e.message ?: e.javaClass.simpleName)
        }
        if (after != target) {
            return AlwaysOnVpnOutcome(
                "always-on=${target ?: "none"}",
                failure = "accepted, and the device reports ${after ?: "none"}",
            )
        }
        val lockdownAfter = try {
            gateway.isLockdownEnabled()
        } catch (e: RuntimeException) {
            return AlwaysOnVpnOutcome("always-on=${target ?: "none"}", failure = "lockdown could not be read back")
        }
        if (wanted && lockdownAfter) {
            // Reported as a failure rather than logged, because this is the state the whole class
            // exists to prevent and the parent's console is where it has to surface.
            return AlwaysOnVpnOutcome(
                "always-on=$ownPackage",
                failure = "the device reports lockdown ON, which is the state that can leave it with no network",
            )
        }
        return AlwaysOnVpnOutcome(
            if (wanted) "always-on=$ownPackage lockdown=false" else "always-on=none",
        )
    }

    companion object {
        /** See the class comment. There is no code path in this project that sets this to true. */
        const val LOCKDOWN = false
    }
}
