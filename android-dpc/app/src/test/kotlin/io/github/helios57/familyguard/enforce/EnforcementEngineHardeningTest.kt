package io.github.helios57.familyguard.enforce

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The two properties that must hold for every input, not just for the twenty shared vectors: the
 * phone stays wipeable, and the engine refuses input it would otherwise have to guess at.
 *
 * Both are asserted on the server side too. They are asserted again here because this is the engine
 * that actually reaches DevicePolicyManager — a server that never sends `no_factory_reset` and a
 * phone that adds one locally produce exactly the same brick.
 */
class EnforcementEngineHardeningTest {

    private fun settings(
        trackingOnly: Boolean = false,
        allowChildInstalls: Boolean = true,
        youtubeBlocked: Boolean = false,
        dailyLimitMinutes: Int = 0,
        bedtimeEnabled: Boolean = false,
    ) = Settings(
        trackingOnly = trackingOnly,
        allowChildInstalls = allowChildInstalls,
        youtubeBlocked = youtubeBlocked,
        dailyLimitMinutes = dailyLimitMinutes,
        bedtimeEnabled = bedtimeEnabled,
        bedtimeStart = "21:00",
        bedtimeEnd = "07:00",
        dnsHost = "family.adguard-dns.com",
        timezone = "Europe/Zurich",
        version = 3,
        blockedPackages = listOf("com.example.game"),
    )

    private val installed = listOf(
        App(pkg = "com.example.game"),
        App(pkg = "com.android.dialer", system = true),
    )

    private fun daytime(s: Settings, used: Int = 0, lock: Boolean = false) = Input(
        settings = s,
        installed = installed,
        usedMinutesToday = used,
        parentLock = lock,
        now = "2026-08-17T14:00:00+02:00",
    )

    /**
     * FR-2.3 / NFR-6. A fully managed device that forbids factory reset can be rescued from a bad
     * policy — or from a control plane that has stopped answering — only through the control plane.
     * Wiping from the recovery menu is the one escape hatch that depends on nothing this project
     * ships, and it stays open.
     */
    @Test
    fun `factory reset is never blocked, in any policy state`() {
        val never = listOf(
            EnforcementEngine.RESTRICTION_FACTORY_RESET,
            EnforcementEngine.RESTRICTION_OUTGOING_CALLS,
            EnforcementEngine.RESTRICTION_SMS,
            EnforcementEngine.RESTRICTION_CREATE_WINDOWS,
        )

        val states = mapOf(
            "idle" to daytime(settings()),
            "hardened" to daytime(settings(allowChildInstalls = false, youtubeBlocked = true)),
            "quota spent" to daytime(settings(dailyLimitMinutes = 30), used = 45),
            "bedtime" to Input(
                settings = settings(bedtimeEnabled = true),
                installed = installed,
                now = "2026-08-17T22:30:00+02:00",
            ),
            "parent lock" to daytime(settings(), lock = true),
            "tracking-only" to daytime(settings(trackingOnly = true)),
        )

        for ((what, input) in states) {
            val state = EnforcementEngine.compute(input)

            // A positive control on every single state, not once for the whole test: an empty
            // restriction list satisfies every absence check below while proving nothing, and it is
            // exactly what a serialisation or early-return bug would produce.
            assertTrue(
                "in the $what state, hardening is not in effect at all — the absence checks below " +
                    "would pass vacuously",
                EnforcementEngine.RESTRICTION_SAFE_BOOT in state.userRestrictions,
            )
            for (r in never) {
                assertTrue(
                    "in the $what state the engine set $r; the phone must stay wipeable from " +
                        "recovery (FR-2.3)",
                    r !in state.userRestrictions,
                )
            }
        }

        // Each state has to be reached, or the sweep above is six readings of the idle one.
        assertEquals("BEDTIME", EnforcementEngine.compute(states.getValue("bedtime")).suspendReason)
        assertEquals("QUOTA", EnforcementEngine.compute(states.getValue("quota spent")).suspendReason)
        assertTrue(EnforcementEngine.compute(states.getValue("parent lock")).locked)
        assertTrue(EnforcementEngine.compute(states.getValue("tracking-only")).suspendedPackages.isEmpty())
        assertTrue(
            EnforcementEngine.compute(states.getValue("hardened")).userRestrictions
                .contains(EnforcementEngine.RESTRICTION_INSTALL_APPS),
        )
    }

    /**
     * The baseline is what the DPC applies before it has ever reached the server — at provisioning
     * compliance, and on a boot that precedes the first sync. If a computed state could ever lack
     * part of it, the device would harden at boot and then *un*-harden on its first successful
     * fetch, which is the wrong direction and entirely silent.
     */
    @Test
    fun `the pre-sync baseline holds in every computed state`() {
        assertTrue(EnforcementEngine.BASELINE_RESTRICTIONS.isNotEmpty())
        assertTrue(
            "the baseline the DPC applies unprompted must not contain a forbidden restriction",
            EnforcementEngine.BASELINE_RESTRICTIONS.none { it in EnforcementEngine.FORBIDDEN_RESTRICTIONS },
        )

        val states = mapOf(
            "idle" to daytime(settings()),
            "tracking-only" to daytime(settings(trackingOnly = true)),
            "bedtime" to Input(
                settings = settings(bedtimeEnabled = true),
                installed = installed,
                now = "2026-08-17T22:30:00+02:00",
            ),
            "everything on" to daytime(
                settings(allowChildInstalls = false, youtubeBlocked = true, dailyLimitMinutes = 30),
                used = 45,
            ),
        )
        for ((what, input) in states) {
            val got = EnforcementEngine.compute(input).userRestrictions
            for (r in EnforcementEngine.BASELINE_RESTRICTIONS) {
                assertTrue("the $what state drops $r from the baseline", r in got)
            }
        }
    }

    /**
     * The floor must leave a way back in.
     *
     * `no_debugging_features` switches `adb` off, and the switch outlives the boot. Measured: an
     * emulator provisioned with this app went from `device` to `offline` the moment the baseline was
     * applied, and did not come back — the AVD had to be wiped. On a phone in a child's pocket that
     * is the intended effect, but only once the phone can be *reached*: a device that has been
     * provisioned and has never synced would then have exactly one escape hatch left, the recovery
     * wipe, and this project deliberately does not spend the last one.
     *
     * It stays in the computed state, where the server can withdraw it, and the second half of this
     * test is what stops the fix from turning into a silent removal of the restriction.
     */
    @Test
    fun `the pre-sync baseline leaves adb reachable, and the synced state does not have to`() {
        assertTrue(
            "no_debugging_features is in the pre-sync baseline: a device that never reaches the " +
                "server would be unreachable by adb as well, with only the recovery wipe left",
            EnforcementEngine.RESTRICTION_DEBUGGING !in EnforcementEngine.BASELINE_RESTRICTIONS,
        )
        assertTrue(
            "no_debugging_features is not in the computed state either — it was removed rather " +
                "than moved, and a synced phone now lets a child enable USB debugging",
            EnforcementEngine.RESTRICTION_DEBUGGING in
                EnforcementEngine.compute(daytime(settings())).userRestrictions,
        )
    }

    /**
     * The engine refuses input it cannot read rather than falling back.
     *
     * A default of UTC for an unreadable time zone would move bedtime by an hour or two and look
     * entirely normal; a bedtime silently treated as disabled because the clock did not parse would
     * be worse. Refusing surfaces as a visible failure to apply policy, which is the only version of
     * this that anyone notices.
     */
    @Test
    fun `unreadable input is refused, never guessed at`() {
        val cases = mapOf(
            "unknown timezone" to settings().copy(timezone = "Mars/Olympus"),
            "empty timezone" to settings().copy(timezone = ""),
            "bedtime start is not a clock" to settings(bedtimeEnabled = true).copy(bedtimeStart = "9pm"),
            "bedtime hour out of range" to settings(bedtimeEnabled = true).copy(bedtimeStart = "25:00"),
            "bedtime minute out of range" to settings(bedtimeEnabled = true).copy(bedtimeEnd = "07:99"),
        )
        for ((what, s) in cases) {
            assertThrows(what, InvalidPolicyInput::class.java) { EnforcementEngine.compute(daytime(s)) }
        }

        assertThrows("now is not RFC 3339", InvalidPolicyInput::class.java) {
            EnforcementEngine.compute(daytime(settings()).copy(now = "17.08.2026 14:00"))
        }

        // A negative control: the same shapes with valid values must compute, or the five cases
        // above could be passing because compute() throws on everything.
        assertEquals(
            "BEDTIME",
            EnforcementEngine.compute(
                Input(
                    settings = settings(bedtimeEnabled = true),
                    installed = installed,
                    now = "2026-08-17T22:30:00+02:00",
                ),
            ).suspendReason,
        )
    }
}
