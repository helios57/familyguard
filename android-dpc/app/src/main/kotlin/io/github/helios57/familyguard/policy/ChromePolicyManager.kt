package io.github.helios57.familyguard.policy

/**
 * Read and write of a managed app's configuration, as plain Kotlin values.
 *
 * The platform's own shape is a `Bundle`, which cannot be constructed off a device; keeping this
 * interface in `String`/`Boolean`/`Int`/`List<String>` is what lets the whole of the bundle's
 * composition — the part with the judgement in it — be a JVM test.
 */
interface ManagedConfigGateway {
    /** What the platform currently holds for [pkg]. Empty when nothing has been set. */
    fun current(pkg: String): Map<String, Any>

    fun set(pkg: String, config: Map<String, Any>)
}

/** @param missing the keys that were written and did not read back, with what was expected. */
data class ChromeOutcome(
    val keys: Int,
    val blocked: Int,
    val failure: String? = null,
    val missing: Map<String, String> = emptyMap(),
) {
    val ok: Boolean get() = failure == null && missing.isEmpty()

    override fun toString(): String = buildString {
        append("keys=").append(keys).append(" blocked=").append(blocked)
        failure?.let { append(" FAILED=").append(it) }
        if (missing.isNotEmpty()) append(" NOT-STORED=").append(missing)
    }
}

/**
 * The single writer of Chrome's managed configuration (FR-6.3, FR-7.3).
 *
 * **Single writer is the whole design.** `setApplicationRestrictions` replaces the bundle; it does
 * not merge. Two components each writing "their" keys is the draft's original bug — the second write
 * silently drops the first's blocklist — so this class composes the complete bundle from a
 * [io.github.helios57.familyguard.enforce.DesiredState] every time and nothing else in the app may call the
 * gateway for Chrome.
 *
 * What the read-back proves is bounded, and the bound is worth stating: `getApplicationRestrictions`
 * returns what the platform stored, so it catches a write that was refused or dropped. It cannot
 * show that Chrome read the bundle, and no API on the device can. That gap is covered by the manual
 * check in the runbook, not by a green test here.
 */
class ChromePolicyManager(private val gateway: ManagedConfigGateway) {

    /**
     * @param blockedDomains the desired state's blocklist, already normalised by the engine.
     * @param safeSearch FR-6.3.
     * @param youtubeRestricted FR-6.3; strict rather than moderate, because moderate is a filter the
     * child can find the edge of.
     * @param neverBlocked hosts that must stay reachable whatever the blocklist says (FR-6.5) —
     * the control plane itself, so a parent who blocks a domain that happens to cover it cannot cut
     * the phone off from the console that would undo it.
     */
    fun apply(
        blockedDomains: List<String>,
        safeSearch: Boolean,
        youtubeRestricted: Boolean,
        neverBlocked: List<String>,
    ): ChromeOutcome {
        val config = compose(blockedDomains, safeSearch, youtubeRestricted, neverBlocked)
        try {
            gateway.set(CHROME_PACKAGE, config)
        } catch (e: RuntimeException) {
            return ChromeOutcome(
                keys = config.size,
                blocked = blockedDomains.size,
                failure = e.message ?: e.javaClass.simpleName,
            )
        }

        val stored = gateway.current(CHROME_PACKAGE)
        val missing = LinkedHashMap<String, String>()
        for ((key, want) in config) {
            val got = stored[key]
            if (!sameValue(want, got)) missing[key] = "expected $want, stored $got"
        }
        return ChromeOutcome(keys = config.size, blocked = blockedDomains.size, missing = missing)
    }

    /**
     * The complete bundle. Every key is written on every apply, including the ones whose value is
     * "off" — an absent key is Chrome's default, not our decision, and FR-6.4 requires that removing
     * a blocked domain restores access rather than leaving the last non-empty list in place.
     */
    fun compose(
        blockedDomains: List<String>,
        safeSearch: Boolean,
        youtubeRestricted: Boolean,
        neverBlocked: List<String>,
    ): Map<String, Any> {
        // "example.com" in Chrome's filter format covers the host and every subdomain, which is what
        // a parent means by blocking a domain. A scheme or a path here would narrow it to exactly
        // one URL, so the engine's normalisation (which strips both) is load-bearing rather than
        // cosmetic.
        val blocklist = blockedDomains.filter { it.isNotBlank() }.distinct().sorted()
        // The allowlist wins over the blocklist in Chrome, which is what makes FR-6.5 enforceable
        // here rather than merely intended.
        val allowlist = neverBlocked.filter { it.isNotBlank() }.distinct().sorted()
        return linkedMapOf(
            KEY_URL_BLOCKLIST to blocklist,
            KEY_URL_ALLOWLIST to allowlist,
            KEY_SAFE_SEARCH to safeSearch,
            KEY_YOUTUBE_RESTRICT to if (youtubeRestricted) YOUTUBE_STRICT else YOUTUBE_OFF,
            // Incognito is not a separate requirement; it is what makes the three above testable at
            // all on a real phone. The blocklist does apply in incognito, but nothing else the
            // parent can see does, and a browser mode whose entire purpose is leaving no trace is
            // not one to leave enabled on a managed child device.
            KEY_INCOGNITO to INCOGNITO_DISABLED,
        )
    }

    /**
     * Value comparison that treats a stored `List` and a stored `Array` as the same thing.
     *
     * The platform round-trips a string list through a `Bundle` as `String[]`, so an `equals` on the
     * two would report every apply as not stored — a permanently red device, which trains the reader
     * to ignore the one that means something.
     */
    private fun sameValue(want: Any, got: Any?): Boolean = when {
        want is List<*> && got is Array<*> -> want == got.toList()
        want is List<*> && got is List<*> -> want == got
        else -> want == got
    }

    companion object {
        /**
         * Chrome's package. Only Chrome: a managed configuration is per package, and guessing at
         * other browsers' key names would write bundles nothing reads. Browsers other than this one
         * are handled by not being installed — the child's phone ships with Chrome, and installing
         * another is what FR-5.4 exists for.
         */
        const val CHROME_PACKAGE = "com.android.chrome"

        // Chrome's enterprise policy names, as published for Android. A typo here is silent: Chrome
        // ignores a key it does not know, so the bundle stores and reads back perfectly while
        // nothing is filtered. `ChromePolicyKeysTest` pins the spelling against this list, which
        // makes the test a record of what was checked against the documentation rather than proof
        // the browser agrees.
        const val KEY_URL_BLOCKLIST = "URLBlocklist"
        const val KEY_URL_ALLOWLIST = "URLAllowlist"
        const val KEY_SAFE_SEARCH = "ForceGoogleSafeSearch"
        const val KEY_YOUTUBE_RESTRICT = "ForceYouTubeRestrict"
        const val KEY_INCOGNITO = "IncognitoModeAvailability"

        const val YOUTUBE_OFF = 0
        const val YOUTUBE_STRICT = 2
        const val INCOGNITO_DISABLED = 1
    }
}
