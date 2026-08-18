package io.github.helios57.familyguard.policy

/** Private DNS as the platform reports it, without the platform's integer constants. */
enum class PrivateDnsMode { OFF, OPPORTUNISTIC, HOSTNAME, UNKNOWN }

/** What a request to change it returned. */
enum class PrivateDnsResult {
    OK,

    /** The platform refused. Nothing changed. */
    FAILED,

    /**
     * The host does not answer DNS-over-TLS. The platform validates before it applies, so this is a
     * refusal and not a broken device — which is exactly what FR-6.5 needs: a typo in the console
     * cannot take the phone off the network.
     */
    HOST_NOT_SERVING,
}

/** The three calls DNS enforcement needs, named so the decision can be tested without a phone. */
interface DnsGateway {
    fun mode(): PrivateDnsMode
    fun host(): String?
    fun setSpecifiedHost(host: String): PrivateDnsResult
    fun setOpportunistic(): PrivateDnsResult
}

data class DnsOutcome(val summary: String, val failure: String? = null) {
    val ok: Boolean get() = failure == null

    override fun toString(): String = if (ok) summary else "$summary FAILED=$failure"
}

/**
 * Locks the device's private DNS to the host the parent chose (FR-6.1, FR-6.2).
 *
 * Device-wide and per-app-invisible: this is the only filtering layer in the product that a
 * sideloaded browser cannot walk around, which is why it is the one the requirements put first.
 *
 * Two decisions are worth stating because the obvious alternative to each is wrong:
 *
 *  - **An empty host means opportunistic, never off.** "No host configured" is the state a family
 *    starts in, and switching private DNS *off* would hand the child's queries to whatever resolver
 *    the café's Wi-Fi hands out — strictly worse than the phone's default, arrived at by a policy
 *    that was trying to be neutral.
 *  - **A host already in effect is not re-applied.** `setGlobalPrivateDnsModeSpecifiedHost`
 *    validates the host over the network before it applies, so calling it on every sync is a DoT
 *    probe every fifteen minutes on a battery-powered device, and one that fails in a tunnel would
 *    report a problem for a device that is correctly configured.
 */
class DnsPolicyManager(private val gateway: DnsGateway) {

    fun apply(desiredHost: String): DnsOutcome {
        val want = desiredHost.trim()
        return if (want.isEmpty()) applyOpportunistic() else applyHost(want)
    }

    private fun applyHost(want: String): DnsOutcome {
        if (gateway.mode() == PrivateDnsMode.HOSTNAME && gateway.host() == want) {
            return DnsOutcome("host=$want (unchanged)")
        }
        val result = try {
            gateway.setSpecifiedHost(want)
        } catch (e: RuntimeException) {
            return DnsOutcome("host=$want", failure = e.message ?: e.javaClass.simpleName)
        }
        if (result != PrivateDnsResult.OK) {
            return DnsOutcome(
                "host=$want",
                failure = when (result) {
                    PrivateDnsResult.HOST_NOT_SERVING ->
                        "\"$want\" does not answer DNS-over-TLS; the device kept its previous resolver"
                    else -> "the platform refused to set the private DNS host"
                },
            )
        }
        // The authority is the platform's own read, not the return code: a device that reports
        // success and comes back in OPPORTUNISTIC is filtering nothing while the console shows a
        // configured host.
        val mode = gateway.mode()
        val host = gateway.host()
        if (mode != PrivateDnsMode.HOSTNAME || host != want) {
            return DnsOutcome("host=$want", failure = "accepted, and the device reports mode=$mode host=$host")
        }
        return DnsOutcome("host=$want")
    }

    private fun applyOpportunistic(): DnsOutcome {
        if (gateway.mode() == PrivateDnsMode.OPPORTUNISTIC) return DnsOutcome("opportunistic (unchanged)")
        val result = try {
            gateway.setOpportunistic()
        } catch (e: RuntimeException) {
            return DnsOutcome("opportunistic", failure = e.message ?: e.javaClass.simpleName)
        }
        if (result != PrivateDnsResult.OK) {
            return DnsOutcome("opportunistic", failure = "the platform refused to clear the private DNS host")
        }
        val mode = gateway.mode()
        if (mode != PrivateDnsMode.OPPORTUNISTIC) {
            return DnsOutcome("opportunistic", failure = "accepted, and the device reports mode=$mode")
        }
        return DnsOutcome("opportunistic")
    }
}
