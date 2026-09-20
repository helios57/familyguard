package io.github.helios57.familyguard.filter

import java.net.Inet4Address
import java.net.InetAddress

/**
 * Which resolvers each network underneath the tunnel is offering — one entry per network.
 *
 * The tunnel advertises its own address as the phone's resolver, so it may only come up if it knows
 * where to forward the queries it does not block. That list arrives asynchronously, one network at
 * a time, from a `ConnectivityManager.NetworkCallback`, and the version of this bookkeeping that
 * lived inline in [AdFilterVpnService] kept ONE list and emptied it on any `onLost`:
 *
 * ```
 * override fun onLost(network: Network) {
 *     underlyingResolvers = emptyList()
 * }
 * ```
 *
 * A phone that drops mobile data while Wi-Fi is up has lost a network it was not using and still
 * has the resolver it was. That line cleared it anyway — and nothing ever refilled it, because the
 * surviving network's link properties do not change when a different network goes away, so no
 * further callback arrives. The tunnel then stood down with *"the network offers no resolver to
 * forward queries to"* on a phone that had one all along, and stayed there: measured on the family
 * phone on 2026-09-20, `ad_filter = t`, 180423 rules compiled, `connectivity = wifi`, heartbeat 30
 * seconds old, `ad_filter_running = f` for hours.
 *
 * So: one entry per network, each removed only by its own loss. Pure and keyed on anything, so the
 * whole thing is assertions rather than a hope about the order callbacks arrive in.
 *
 * It is deliberately NOT the only source — [AdFilterVpnService] asks the platform directly at the
 * moment it decides, and falls back to this. A remembered projection of system state that has no
 * repair path is the defect above; a book that can only ever be a second opinion cannot repeat it.
 */
class ResolverBook<K> {

    private val byNetwork = LinkedHashMap<K, List<String>>()

    /**
     * Record what one network offers.
     *
     * An empty list removes the entry rather than storing emptiness: "this network named no
     * resolver" and "this network is gone" are the same thing to a forwarder, and keeping an empty
     * entry would let it win over a network that has one.
     */
    @Synchronized
    fun learned(network: K, resolvers: List<String>) {
        if (resolvers.isEmpty()) byNetwork.remove(network) else byNetwork[network] = resolvers
    }

    /** Forget one network, and only that one. */
    @Synchronized
    fun lost(network: K) {
        byNetwork.remove(network)
    }

    /**
     * Where to forward, given the network the phone's traffic is actually on.
     *
     * The active network's own resolver first: a resolver reached over a network you are not on is
     * a query that goes nowhere, and on a phone that has just changed networks the two lists are
     * different. Anything else known is better than nothing, because "nothing" is a tunnel that
     * does not come up at all.
     */
    @Synchronized
    fun forwardTo(active: K?): List<String> =
        byNetwork[active] ?: byNetwork.values.firstOrNull() ?: emptyList()

    companion object {

        /**
         * The resolver addresses a link offers, in the order to try them, or empty.
         *
         * [exclude] is the tunnel's own address. It appears here whenever the platform hands back
         * the link properties of a network the tunnel is already on, and forwarding a query to the
         * address the query arrived at is the loop that pegs the CPU until the battery is flat.
         *
         * IPv4 only, and that is a restriction rather than an oversight: an upstream this cannot
         * reach is worse than no tunnel at all, because the tunnel is the phone's resolver while it
         * is up, and nothing in the watchdog notices a tunnel that carries queries nobody answers.
         * Including IPv6 is a change that has to be proven against a real socket and a real
         * network, not reasoned about.
         */
        fun usable(dns: List<InetAddress>, exclude: String): List<String> =
            dns.filterIsInstance<Inet4Address>()
                .mapNotNull { it.hostAddress }
                .filter { it.isNotBlank() && it != exclude }
                .distinct()
    }
}
