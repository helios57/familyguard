package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.policy.AppGateway
import io.github.helios57.familyguard.policy.AppSuspensionManager
import io.github.helios57.familyguard.policy.ChromePolicyManager
import io.github.helios57.familyguard.policy.DnsGateway
import io.github.helios57.familyguard.policy.DnsPolicyManager
import io.github.helios57.familyguard.policy.HardeningManager
import io.github.helios57.familyguard.policy.ManagedConfigGateway
import io.github.helios57.familyguard.policy.PrivateDnsMode
import io.github.helios57.familyguard.policy.PrivateDnsResult
import io.github.helios57.familyguard.policy.RestrictionGateway
import io.github.helios57.familyguard.policy.compliantClock
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** An applier whose result the test dictates, and which records that it was reached at all. */
private class ScriptedApplier(
    private val outcome: ApplyOutcome,
    private val throws: RuntimeException? = null,
) : StateApplier {
    var calls = 0
        private set

    override fun apply(state: DesiredState): ApplyOutcome {
        calls++
        throws?.let { throw it }
        return outcome
    }
}

/** The same platform stand-in shape [io.github.helios57.familyguard.policy.HardeningManagerTest] uses. */
private class FakeGateway(
    initial: Set<String> = emptySet(),
    private val ignore: Set<String> = emptySet(),
    private val throwOn: Set<String> = emptySet(),
) : RestrictionGateway {
    private val state = initial.toMutableSet()

    override fun current(): Set<String> = state.toSet()

    override fun add(key: String) {
        if (key in throwOn) throw SecurityException("$key is not supported on this build")
        if (key !in ignore) state += key
    }

    override fun clear(key: String) {
        if (key in throwOn) throw SecurityException("$key cannot be cleared on this build")
        if (key !in ignore) state -= key
    }
}

class CompositeApplierTest {

    private val state = DesiredState(policyVersion = 3)
    private val clean = ApplyOutcome("nothing to do")

    @Test
    fun `every applier runs, whatever the ones before it reported`() {
        val failing = ScriptedApplier(ApplyOutcome("dns", mapOf("dns" to "no such host")))
        val after = ScriptedApplier(clean)

        val outcome = CompositeApplier(listOf("dns" to failing, "apps" to after)).apply(state)

        // The point of the whole class: a device that cannot set its DNS host must still suspend
        // apps at bedtime. An applier that never ran is an unmanaged half of a managed phone.
        assertEquals("the applier after a failing one did not run", 1, after.calls)
        assertFalse(outcome.ok)
        assertEquals(mapOf("dns/dns" to "no such host"), outcome.problems)
    }

    @Test
    fun `an applier that throws is recorded, and the rest still run`() {
        val boom = ScriptedApplier(clean, throws = IllegalStateException("bad cast"))
        val after = ScriptedApplier(clean)

        val outcome = CompositeApplier(listOf("chrome" to boom, "apps" to after)).apply(state)

        assertEquals(1, after.calls)
        assertEquals(mapOf("chrome/!" to "bad cast"), outcome.problems)
    }

    @Test
    fun `two appliers failing at the same key both survive the merge`() {
        val dns = ScriptedApplier(ApplyOutcome("dns", mapOf("youtube.com" to "not blocked")))
        val chrome = ScriptedApplier(ApplyOutcome("chrome", mapOf("youtube.com" to "not in policy")))

        val outcome = CompositeApplier(listOf("dns" to dns, "chrome" to chrome)).apply(state)

        // Without the applier-name prefix one of these two silently replaces the other, and the
        // parent is told about one failure where there were two.
        assertEquals(
            mapOf("dns/youtube.com" to "not blocked", "chrome/youtube.com" to "not in policy"),
            outcome.problems,
        )
    }

    @Test
    fun `a clean run reports every applier's summary and no problems`() {
        val outcome = CompositeApplier(
            listOf("dns" to ScriptedApplier(ApplyOutcome("host set")), "apps" to ScriptedApplier(ApplyOutcome("2 suspended"))),
        ).apply(state)

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("dns[host set] apps[2 suspended]", outcome.summary)
    }
}

class RestrictionApplierTest {

    private val safeBoot = EnforcementEngine.RESTRICTION_SAFE_BOOT
    private val debugging = EnforcementEngine.RESTRICTION_DEBUGGING
    private val installApps = EnforcementEngine.RESTRICTION_INSTALL_APPS
    private val factoryReset = EnforcementEngine.RESTRICTION_FACTORY_RESET

    private fun applier(gateway: RestrictionGateway) = RestrictionApplier(HardeningManager(gateway, compliantClock()))

    @Test
    fun `it applies what the server asked for and clears what it did not`() {
        val gateway = FakeGateway(initial = setOf(installApps))
        val outcome = applier(gateway).apply(DesiredState(userRestrictions = listOf(safeBoot)))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("added=1 cleared=1", outcome.summary)
        assertEquals(setOf(safeBoot), gateway.current())
    }

    @Test
    fun `a restriction the platform accepted and did not apply is a problem`() {
        val gateway = FakeGateway(ignore = setOf(debugging))
        val outcome = applier(gateway).apply(DesiredState(userRestrictions = listOf(safeBoot, debugging)))

        assertFalse("the applier believed the call it made", outcome.ok)
        assertEquals(mapOf(debugging to "requested, accepted, and not in effect"), outcome.problems)
        // Positive control: the other one really landed, so this is a finding about one call and
        // not about a gateway that ignores everything.
        assertTrue(safeBoot in gateway.current())
    }

    /**
     * A refused restriction is also a missing one — it was requested and is not in effect — so both
     * of the applier's two sources name the same key. The reported reason has to be the specific
     * half: "the platform threw" and "the platform accepted it and did nothing" send a parent
     * looking in different places, and only one of them happened.
     */
    @Test
    fun `a restriction the platform refused reports why, not just that it is absent`() {
        val gateway = FakeGateway(throwOn = setOf(debugging))
        val outcome = applier(gateway).apply(DesiredState(userRestrictions = listOf(debugging)))

        assertFalse(outcome.ok)
        assertEquals(setOf(debugging), outcome.problems.keys)
        val reason = outcome.problems.getValue(debugging)
        assertTrue(reason, reason.startsWith("add: ") && reason.contains("not supported"))
        assertFalse("the refusal was reported as an acceptance", reason.contains("accepted"))
    }

    /**
     * FR-2.3 / NFR-6, at the layer that reports rather than the layer that decides. Nothing this app
     * does sets `no_factory_reset`, so a device carrying it was put there by something else — and a
     * phone that cannot be wiped from recovery has lost the escape hatch this project keeps open on
     * purpose. The clear is attempted and, here, ignored; the applier must say so out loud instead
     * of reporting a clean apply and letting the device claim the policy version.
     */
    @Test
    fun `a forbidden restriction that will not clear is a problem`() {
        val gateway = FakeGateway(initial = setOf(factoryReset), ignore = setOf(factoryReset))
        val outcome = applier(gateway).apply(DesiredState(userRestrictions = listOf(safeBoot)))

        assertFalse("the device is not wipeable and the apply reported clean", outcome.ok)
        assertEquals(
            mapOf(factoryReset to "forbidden restriction still in effect"),
            outcome.problems,
        )
    }

    @Test
    fun `a forbidden restriction is never requested, whatever the server sends`() {
        val gateway = FakeGateway()
        val outcome = applier(gateway)
            .apply(DesiredState(userRestrictions = listOf(safeBoot, factoryReset)))

        assertTrue(outcome.toString(), outcome.ok)
        assertFalse("the phone was made un-wipeable by a server response", factoryReset in gateway.current())
        assertEquals(setOf(safeBoot), gateway.current())
    }
}

/** A suspension gateway that does exactly what it is told, so the applier is what is measured. */
private class RecordingAppGateway(
    private val present: Set<String>,
    private val ignore: Set<String> = emptySet(),
    alreadySuspended: Set<String> = emptySet(),
) : AppGateway {
    private val isSuspended = alreadySuspended.toMutableSet()
    private val isHidden = mutableSetOf<String>()

    override fun installed(): Set<String> = present

    override fun suspended(): Set<String> = isSuspended.toSet()

    override fun hidden(): Set<String> = isHidden.toSet()

    override fun setSuspended(packages: List<String>, suspended: Boolean): Map<String, String> {
        packages.filter { it !in ignore }.forEach { if (suspended) isSuspended += it else isSuspended -= it }
        return emptyMap()
    }

    override fun setHidden(pkg: String, hidden: Boolean): Boolean {
        if (pkg !in ignore) { if (hidden) isHidden += pkg else isHidden -= pkg }
        return true
    }
}

class AppApplierTest {

    private val own = "io.github.helios57.familyguard"
    private val dialer = "com.android.dialer"

    private fun applier(gateway: AppGateway) =
        AppApplier(AppSuspensionManager(gateway, setOf(own, dialer), ownPackage = own))

    @Test
    fun `it applies both halves of the desired state`() {
        val gateway = RecordingAppGateway(present = setOf("com.game", "com.chat", own))
        val outcome = applier(gateway).apply(
            DesiredState(suspendedPackages = listOf("com.game", "com.chat"), hiddenPackages = listOf("com.game")),
        )

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(setOf("com.game", "com.chat"), gateway.suspended())
        assertEquals(setOf("com.game"), gateway.hidden())
    }

    @Test
    fun `the whole desired set is passed every time, so a device repairs itself`() {
        val gateway = RecordingAppGateway(present = setOf("com.game", own))
        val a = applier(gateway)

        a.apply(DesiredState(suspendedPackages = listOf("com.game")))
        // The parent deleted the rule. Nothing in the second state mentions the package at all, and
        // a delta-based applier would leave it suspended forever.
        val outcome = a.apply(DesiredState(suspendedPackages = emptyList()))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(emptySet<String>(), gateway.suspended())
    }

    @Test
    fun `accepted and not in effect becomes a problem naming the package`() {
        val gateway = RecordingAppGateway(present = setOf("com.game", own), ignore = setOf("com.game"))
        val outcome = applier(gateway).apply(DesiredState(suspendedPackages = listOf("com.game")))

        assertFalse(outcome.ok)
        assertEquals("suspension requested, accepted, and not in effect", outcome.problems["com.game"])
    }

    @Test
    fun `a restrained critical package is a problem`() {
        // Something else on the device suspended the dialer, and the platform accepts the release
        // without carrying it out. Nothing in the desired state mentions the dialer at all, so this
        // is the applier noticing a device it did not put in that state.
        val gateway = RecordingAppGateway(
            present = setOf(dialer, own),
            ignore = setOf(dialer),
            alreadySuspended = setOf(dialer),
        )

        val outcome = applier(gateway).apply(DesiredState())

        assertFalse(outcome.ok)
        assertEquals(
            "critical package is suspended or hidden on this device",
            outcome.problems[dialer],
        )
    }

    @Test
    fun `a critical package restrained by an older policy is released`() {
        // The positive half of the test above: when the platform does carry the release out, the
        // applier repairs the phone and reports clean. Without both, "reports a problem" could just
        // mean the gateway never releases anything.
        val gateway = RecordingAppGateway(present = setOf(dialer, own), alreadySuspended = setOf(dialer))

        val outcome = applier(gateway).apply(DesiredState())

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(emptySet<String>(), gateway.suspended())
    }

    @Test
    fun `a package that is not installed is not a problem`() {
        val gateway = RecordingAppGateway(present = setOf(own))
        val outcome = applier(gateway).apply(DesiredState(suspendedPackages = listOf("com.not.here")))

        assertTrue(outcome.toString(), outcome.ok)
    }
}

/** Stores what it is given, exactly as given — the round-trip is covered in ChromePolicyManagerTest. */
private class EchoConfigGateway(private val drop: Set<String> = emptySet()) : ManagedConfigGateway {
    private var stored: Map<String, Any> = emptyMap()

    override fun current(pkg: String): Map<String, Any> = stored

    override fun set(pkg: String, config: Map<String, Any>) {
        stored = config.filterKeys { it !in drop }
    }
}

class ChromeApplierTest {

    private val console = "guard.example.com"

    @Test
    fun `it writes the desired state's browser half`() {
        val gateway = EchoConfigGateway()
        val outcome = ChromeApplier(ChromePolicyManager(gateway), listOf(console)).apply(
            DesiredState(
                blockedDomains = listOf("example.com"),
                safeSearch = true,
                youtubeRestrictedMode = true,
            ),
        )

        assertTrue(outcome.toString(), outcome.ok)
        val stored = gateway.current(ChromePolicyManager.CHROME_PACKAGE)
        assertEquals(listOf("example.com"), stored[ChromePolicyManager.KEY_URL_BLOCKLIST])
        assertEquals(listOf(console), stored[ChromePolicyManager.KEY_URL_ALLOWLIST])
        assertEquals(true, stored[ChromePolicyManager.KEY_SAFE_SEARCH])
    }

    @Test
    fun `a key that did not store is a problem`() {
        val gateway = EchoConfigGateway(drop = setOf(ChromePolicyManager.KEY_SAFE_SEARCH))
        val outcome = ChromeApplier(ChromePolicyManager(gateway), listOf(console))
            .apply(DesiredState(safeSearch = true))

        assertFalse(outcome.ok)
        assertTrue(outcome.problems.containsKey(ChromePolicyManager.KEY_SAFE_SEARCH))
    }

    @Test
    fun `the allowlist is the control plane's host, not its URL`() {
        // Chrome's filter format is host-based. Handing it a full URL narrows the entry to one exact
        // address, so the console's own API calls would not be covered by it.
        assertEquals(listOf("guard.example.com"), ChromeApplier.allowlistFor("https://guard.example.com/api/v1"))
        assertEquals(listOf("guard.example.com"), ChromeApplier.allowlistFor("  https://guard.example.com  "))
    }

    @Test
    fun `a URL with no usable host yields no allowlist entry at all`() {
        // An empty-string entry is not neutral: Chrome reads it as a pattern matching everything,
        // which turns the one entry meant to protect the console into a switch that disables the
        // whole blocklist.
        assertEquals(emptyList<String>(), ChromeApplier.allowlistFor(""))
        assertEquals(emptyList<String>(), ChromeApplier.allowlistFor("not a url"))
        assertEquals(emptyList<String>(), ChromeApplier.allowlistFor("http://"))
        assertEquals(emptyList<String>(), ChromeApplier.allowlistFor("::::"))
    }
}

class DnsApplierTest {

    private class Gateway(
        private var mode: PrivateDnsMode = PrivateDnsMode.OPPORTUNISTIC,
        private var host: String? = null,
        private val accept: PrivateDnsResult = PrivateDnsResult.OK,
    ) : DnsGateway {
        override fun mode() = mode
        override fun host() = host
        override fun setSpecifiedHost(host: String): PrivateDnsResult {
            if (accept == PrivateDnsResult.OK) { mode = PrivateDnsMode.HOSTNAME; this.host = host }
            return accept
        }
        override fun setOpportunistic(): PrivateDnsResult {
            if (accept == PrivateDnsResult.OK) { mode = PrivateDnsMode.OPPORTUNISTIC; host = null }
            return accept
        }
    }

    @Test
    fun `it sets the desired state's private DNS host`() {
        val gateway = Gateway(mode = PrivateDnsMode.OFF)
        val outcome = DnsApplier(DnsPolicyManager(gateway))
            .apply(DesiredState(privateDnsHost = "family.dns.example"))

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("family.dns.example", gateway.host())
    }

    @Test
    fun `a refusal is a problem under a key a parent can act on`() {
        val gateway = Gateway(mode = PrivateDnsMode.OFF, accept = PrivateDnsResult.HOST_NOT_SERVING)
        val outcome = DnsApplier(DnsPolicyManager(gateway))
            .apply(DesiredState(privateDnsHost = "typo.example"))

        assertFalse(outcome.ok)
        assertTrue(outcome.problems.getValue("private_dns").contains("typo.example"))
    }
}
