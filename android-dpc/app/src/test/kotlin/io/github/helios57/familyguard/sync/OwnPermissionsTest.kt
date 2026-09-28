package io.github.helios57.familyguard.sync

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * A device owner grants itself its runtime permissions, and must do it only when one is missing.
 *
 * Measured on a family phone (Galaxy S20, Android 13, 2026-09-28): every grant of a location permission
 * makes the permission controller post "FamilyGuard has location access — your organisation allows
 * FamilyGuard to access your location", at alerting importance. The grant ran on every start of the
 * connection service, and 0.6.27's poll and mode alarms start it every few minutes — so the child's
 * phone chimed with it every three minutes. (The API 37 emulator posts it once and never again, which
 * is why this is a unit test: the device there cannot tell the two builds apart.)
 */
class OwnPermissionsTest {

    private val needed = listOf("FINE", "COARSE", "BACKGROUND", "NOTIFY")

    @Test
    fun `a permission the policy grants and the app holds is left alone`() {
        val policyGranted = setOf("FINE", "COARSE", "BACKGROUND", "NOTIFY")
        val held = setOf("FINE", "COARSE", "BACKGROUND", "NOTIFY")
        assertEquals(emptyList<String>(), OwnPermissions.toGrant(needed, { it in policyGranted }, { it in held }))
    }

    @Test
    fun `a missing grant, or one revoked by hand, is granted again`() {
        val policyGranted = setOf("FINE", "COARSE", "NOTIFY")
        val held = setOf("FINE", "BACKGROUND", "NOTIFY")
        assertEquals(listOf("COARSE", "BACKGROUND"), OwnPermissions.toGrant(needed, { it in policyGranted }, { it in held }))
    }
}
