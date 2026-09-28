package io.github.helios57.familyguard.sync

/**
 * Which of its runtime permissions a device owner must grant itself now: those the policy does not
 * already grant, or that the app does not hold (revoked by hand since). Nothing else.
 *
 * Granting one that is already granted is not free. Measured on a Galaxy S20 (Android 13,
 * 2026-09-28): each grant of a location permission makes the permission controller post "FamilyGuard
 * has location access" at alerting importance, and the grant ran on every start of the connection
 * service — every few minutes once the phone polls — so the child's phone chimed every three minutes.
 */
object OwnPermissions {
    fun toGrant(needed: List<String>, policyGranted: (String) -> Boolean, held: (String) -> Boolean): List<String> =
        needed.filter { !policyGranted(it) || !held(it) }
}
