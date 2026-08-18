package io.github.helios57.familyguard.policy

/**
 * The two calls that fix the device's clock, named so the decision can be tested without a phone.
 *
 * Both are allowed to throw whatever the platform throws; [ClockPolicyManager] is what decides how
 * a refusal is reported. Neither returns a status, because a status is what the platform *claims* —
 * the only thing this product treats as evidence is the value read back afterwards.
 */
interface ClockGateway {
    /** Whether the device takes its time from the network. */
    fun autoTimeEnabled(): Boolean

    /** Turns automatic network time on. */
    fun enableAutoTime()
}

/**
 * What one clock enforcement did. [failure] is null exactly when the clock is now automatic.
 *
 * There is no third state here on purpose. "Could not tell" is a failure: a phone whose clock this
 * app cannot read is a phone whose quota it cannot trust, and folding that into a pass is the exact
 * shape of green this repository exists to refuse.
 */
data class ClockOutcome(val summary: String, val failure: String? = null) {
    val ok: Boolean get() = failure == null

    override fun toString(): String = if (ok) summary else "$summary FAILED=$failure"
}

/**
 * Requires automatic network time (FR-2.2), so the clock cannot be rolled back to defeat a quota.
 *
 * This is the second half of a defence whose first half is a restriction. `DISALLOW_CONFIG_DATE_TIME`
 * (FR-2.1) stops the child from *changing* the setting, and it is in the baseline — but it freezes
 * whatever state the device was in when it was applied. A phone provisioned with automatic time
 * already off keeps a clock nobody is correcting, and the restriction then locks that in: the child
 * sets the clock back an hour every evening and the daily quota is never reached, the bedtime window
 * is never entered, and nothing anywhere is red. The console would show a child who simply stopped
 * using their phone at 20:00.
 *
 * So the value is asserted, not assumed, and it is asserted at exactly the two moments FR-2 names —
 * provisioning and every boot — by living inside [HardeningManager.applyBaseline].
 *
 * **It is not part of the desired state a sync applies.** Automatic time is not a user restriction,
 * so the server never sends it, and adding a per-sync write would mean a settings-provider write
 * every fifteen minutes for a value that can only be changed by an admin the child does not have.
 */
class ClockPolicyManager(private val gateway: ClockGateway) {

    fun apply(): ClockOutcome {
        val before = try {
            gateway.autoTimeEnabled()
        } catch (e: RuntimeException) {
            return ClockOutcome(
                "auto-time",
                failure = "could not be read: " + (e.message ?: e.javaClass.simpleName),
            )
        }
        // Not re-applied when it is already on. The write goes to the settings provider and is
        // pointless when nothing changes, and a call made on every boot is a call whose failures
        // nobody reads by the tenth one.
        if (before) return ClockOutcome("auto-time=on (unchanged)")

        try {
            gateway.enableAutoTime()
        } catch (e: RuntimeException) {
            return ClockOutcome(
                "auto-time=off",
                failure = "could not be turned on: " + (e.message ?: e.javaClass.simpleName),
            )
        }

        // The read-back, which is the point of the whole class. `setAutoTimeEnabled` returns void
        // and does not throw when the value does not take, so a device whose OEM ignores it looks
        // identical to one that complied — and the difference is a quota that can be walked around.
        val after = try {
            gateway.autoTimeEnabled()
        } catch (e: RuntimeException) {
            return ClockOutcome(
                "auto-time",
                failure = "was set, and could not be read back: " + (e.message ?: e.javaClass.simpleName),
            )
        }
        return if (after) {
            ClockOutcome("auto-time=on (was off)")
        } else {
            ClockOutcome(
                "auto-time=off",
                failure = "the call was accepted and automatic time is still off; this device's " +
                    "clock can be set by hand, so a daily quota can be walked around",
            )
        }
    }
}
