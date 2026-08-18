package io.github.helios57.familyguard.policy

/**
 * The three calls `HardeningManager` needs from the platform, named so the manager can be tested
 * without one.
 *
 * [current] is deliberately a read of what is *in effect*, not a memory of what this app asked for.
 * `addUserRestriction` is a request: on some OEM builds a restriction that is not supported is
 * accepted and never applied, and an applier that trusted its own bookkeeping would report a
 * hardened device forever. Reading the state back is the only way to tell those apart.
 */
interface RestrictionGateway {
    fun current(): Set<String>
    fun add(key: String)
    fun clear(key: String)
}
