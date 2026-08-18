package io.github.helios57.familyguard.enforce

import android.os.UserManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Every restriction key this project ships must be the string the platform actually knows.
 *
 * This is the cheapest guard in the repo and it exists because the failure it catches is silent.
 * `DevicePolicyManager.addUserRestriction` accepts a key it does not recognise, logs, and applies
 * nothing: no exception, no error return. A misspelled key therefore produces a device that reports
 * itself hardened, a console that shows the control as on, and a child who can change the setting.
 *
 * It is not hypothetical. `RESTRICTION_PRIVATE_DNS` was `no_config_private_dns` — the spelling the
 * `no_` convention every *other* key in the set follows would suggest — on both the Go and the
 * Kotlin side, and in the three shared vectors. Everything agreed with everything: 24 JVM tests, the
 * 20 shared vectors, the Go suite and the e2e journeys were all green, because they compared our
 * spelling against our spelling. The emulator was the first thing in the project to ask the
 * platform, and `dumpsys user` had no such restriction in any section.
 * `DISALLOW_CONFIG_PRIVATE_DNS` is `disallow_config_private_dns`.
 *
 * `EnforcementEngine` deliberately has no Android import — that is what lets it run on the JVM in
 * milliseconds and be diffed against the Go engine by `vectors.json`. This test is the seam that
 * makes copying the strings safe rather than merely convenient, and it runs on the JVM too: these
 * are Java compile-time constants, so the reference is inlined and no Android runtime is involved.
 *
 * The chain, end to end: Go engine ≡ `vectors.json` ≡ Kotlin engine ≡ `android.os.UserManager`.
 * Break any link and something goes red.
 */
class RestrictionKeysMatchThePlatformTest {

    /**
     * Every key, against the platform constant it stands for. Written out one by one on purpose: a
     * loop over `UserManager`'s fields would compare the platform to itself.
     */
    private val bindings: Map<String, Pair<String, String>> = mapOf(
        "RESTRICTION_SAFE_BOOT" to (EnforcementEngine.RESTRICTION_SAFE_BOOT to UserManager.DISALLOW_SAFE_BOOT),
        "RESTRICTION_DEBUGGING" to
            (EnforcementEngine.RESTRICTION_DEBUGGING to UserManager.DISALLOW_DEBUGGING_FEATURES),
        "RESTRICTION_PRIVATE_DNS" to
            (EnforcementEngine.RESTRICTION_PRIVATE_DNS to UserManager.DISALLOW_CONFIG_PRIVATE_DNS),
        "RESTRICTION_DATE_TIME" to
            (EnforcementEngine.RESTRICTION_DATE_TIME to UserManager.DISALLOW_CONFIG_DATE_TIME),
        "RESTRICTION_ADD_USER" to (EnforcementEngine.RESTRICTION_ADD_USER to UserManager.DISALLOW_ADD_USER),
        "RESTRICTION_UNKNOWN_SOURCES" to
            (EnforcementEngine.RESTRICTION_UNKNOWN_SOURCES to UserManager.DISALLOW_INSTALL_UNKNOWN_SOURCES),
        "RESTRICTION_INSTALL_APPS" to
            (EnforcementEngine.RESTRICTION_INSTALL_APPS to UserManager.DISALLOW_INSTALL_APPS),
        "RESTRICTION_UNINSTALL_APPS" to
            (EnforcementEngine.RESTRICTION_UNINSTALL_APPS to UserManager.DISALLOW_UNINSTALL_APPS),
        "RESTRICTION_FACTORY_RESET" to
            (EnforcementEngine.RESTRICTION_FACTORY_RESET to UserManager.DISALLOW_FACTORY_RESET),
        "RESTRICTION_OUTGOING_CALLS" to
            (EnforcementEngine.RESTRICTION_OUTGOING_CALLS to UserManager.DISALLOW_OUTGOING_CALLS),
        "RESTRICTION_CREATE_WINDOWS" to
            (EnforcementEngine.RESTRICTION_CREATE_WINDOWS to UserManager.DISALLOW_CREATE_WINDOWS),
        "RESTRICTION_SMS" to (EnforcementEngine.RESTRICTION_SMS to UserManager.DISALLOW_SMS),
    )

    @Test
    fun `every restriction key is the platform's own`() {
        for ((name, pair) in bindings) {
            val (ours, platform) = pair
            assertEquals("$name is not the key android.os.UserManager uses", platform, ours)
        }

        // The negative control. Every assertion above is `assertEquals` between two strings, and if
        // the platform constants were somehow all empty — a stubbed android.jar returning "" rather
        // than inlining, which is exactly the environment these tests run in — the loop would agree
        // on everything while comparing nothing.
        assertTrue(
            "the platform constants read back empty, so the comparisons above were vacuous",
            bindings.values.none { it.second.isEmpty() },
        )
        assertNotEquals(
            "no_config_private_dns",
            UserManager.DISALLOW_CONFIG_PRIVATE_DNS,
        )
    }

    /**
     * A key added to the engine and forgotten here would be un-bound, and un-bound is the state this
     * whole file exists to make impossible. Reflection over the object's own fields is what closes
     * it: the list above cannot silently fall behind the constants it covers.
     */
    @Test
    fun `no restriction constant escapes the binding`() {
        val declared = EnforcementEngine::class.java.declaredFields
            .filter { it.name.startsWith("RESTRICTION_") && it.type == String::class.java }
            .map { it.name }
            .toSortedSet()

        // A positive control on the reflection itself. Kotlin `const val` in an object compiles to a
        // static field on the class, but that is a compiler detail; if it ever stops being true this
        // set is empty and the comparison below passes by finding nothing on either side.
        assertTrue("reflection found no RESTRICTION_* fields at all", declared.size >= 8)

        assertEquals(
            "a restriction constant is not bound to a platform key",
            declared,
            bindings.keys.toSortedSet(),
        )
    }
}
