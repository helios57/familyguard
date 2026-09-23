package io.github.helios57.familyguard.filter

/** What a parent asked for. Everything else about the tunnel is derived from this. */
data class FilterPolicy(
    val enabled: Boolean = false,
    val mode: RouteMode = RouteMode.FULL,
)

/** Whether to run a tunnel at all, and with what. */
sealed interface TunnelDecision {

    data class Run(val mode: RouteMode, val upstream: List<String>) : TunnelDecision

    /**
     * Do not run, and say why in words a parent could be shown.
     *
     * A reason rather than a bare `null` because every one of these is a state the console has to
     * be able to explain: *"the filter is on but nothing is being filtered"* is the question this
     * answers, and the alternative is a support conversation that starts from nothing.
     */
    data class Stand(val reason: String) : TunnelDecision
}

/**
 * The one place that decides whether the tunnel comes up.
 *
 * Every refusal here is a way the phone keeps working. That is the whole design: this is the
 * feature that can take a child's internet away, so the question it asks is not *"may I run"* but
 * *"is there any reason not to"* — and each of the four below is a real failure that would
 * otherwise present as a phone with no network and no explanation.
 *
 * Pure, so all four are assertions rather than a hope. The service does nothing but carry the
 * answer out.
 */
object TunnelPlan {

    fun decide(
        policy: FilterPolicy,
        /** Resolvers on the **underlying** network — never the tunnel's own, never a chosen one. */
        upstream: List<String>,
        /** Rules in the compiled index. Zero means no list has been fetched yet. */
        ruleCount: Int,
        /** True once [TunnelWatchdog] has given up on this configuration. */
        stoodDown: Boolean = false,
    ): TunnelDecision {
        if (!policy.enabled) return TunnelDecision.Stand("the filter is switched off for this child")
        if (stoodDown) {
            // The watchdog has already seen this configuration carry nothing, twice. Coming back up
            // into the same failure is a phone that never works and a battery that never lasts.
            return TunnelDecision.Stand("the tunnel carried no traffic and was stood down")
        }
        if (ruleCount == 0) {
            // A tunnel with an empty index blocks nothing and carries everything, so it is pure
            // added risk: every packet on the phone now depends on this code for no benefit at all.
            return TunnelDecision.Stand("no filter list has been compiled yet")
        }
        if (upstream.isEmpty()) {
            // The tunnel takes over DNS by advertising its own resolver address. With nowhere to
            // forward the queries it does not filter, name resolution stops dead — which is the
            // whole phone, not just advertising.
            return TunnelDecision.Stand("the network offers no resolver to forward queries to")
        }
        return TunnelDecision.Run(policy.mode, upstream)
    }

    /**
     * Whether a tunnel running [running] can be left exactly as it is when the plan is now [next]
     * (FR-6.12).
     *
     * Every sync re-applies the whole policy, and for months that meant every sync tore the tunnel
     * down and built it again — every five minutes with the screen on, and on every change a
     * parent made. A rebuild closes every connection the tunnel is carrying, so an app streaming or
     * loading a page lost it mid-way, and the phone's report, sent in the same breath, caught the
     * tunnel between the two runs and said the filter had never started. Nothing in a re-applied
     * policy that leaves this decision unchanged needs a new tunnel: the rules are swapped into the
     * running engine in place (FilterState.refresh), and the decision already holds everything the
     * tunnel is built from — the route and where queries go.
     */
    fun keeps(running: TunnelDecision.Run?, next: TunnelDecision): Boolean = running != null && next == running
}
