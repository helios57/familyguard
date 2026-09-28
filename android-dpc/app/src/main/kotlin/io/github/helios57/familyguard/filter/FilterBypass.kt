package io.github.helios57.familyguard.filter

/**
 * The apps whose traffic goes around the ad filter entirely (FR-26.4), so the filter spends nothing
 * on it: every packet the tunnel carries is read, parsed and re-sent by this process, and traffic that
 * cannot contain an ad is traffic that costs battery for nothing.
 *
 * The owner's rule, 2026-09-28: *"It's also ok to whitelist some traffic to go around if it's safe and
 * guaranteed not to contain ads."* So a package is here only when that guarantee is structural:
 *
 * - named below, one by one, each with its reason; or
 * - the phone's default dialer or SMS app **and** shipped with the phone. Being the default is not
 *   enough on its own: a child can make an ad-supported SMS app the default.
 *
 * Deliberately absent: WhatsApp, which has shown ads in Status since 2025.
 */
object FilterBypass {

    /** Excluded when installed. The list is short on purpose: each entry is a promise. */
    val NAMED: List<String> = listOf(
        // Voice and SMS over IP, Wi-Fi calling: the carrier's IMS stack. Samsung's, then Google's
        // Carrier Services. A tunnel in this path can only cost a call.
        "com.sec.imsservice",
        "com.google.android.ims",
        // Signal: no ads, by its foundation's charter.
        "org.thoughtcrime.securesms",
        // Threema, in all four of its published packages: paid or self-hosted, no ads.
        "ch.threema.app",
        "ch.threema.app.libre",
        "ch.threema.app.work",
        "ch.threema.app.onprem",
        // Google Play services — the owner's decision of 2026-09-28, taken with its risk stated: Google's
        // ads SDK runs inside Play services, so ads it fetches on behalf of other apps are no longer
        // filtered. It is also the package that carries push (FR-26.3).
        "com.google.android.gms",
    )

    fun packages(
        isInstalled: (String) -> Boolean,
        isSystem: (String) -> Boolean,
        defaultDialer: String?,
        defaultSms: String?,
    ): List<String> {
        val shippedDefaults = listOfNotNull(defaultDialer, defaultSms).filter { isInstalled(it) && isSystem(it) }
        return (NAMED.filter(isInstalled) + shippedDefaults).distinct()
    }
}
