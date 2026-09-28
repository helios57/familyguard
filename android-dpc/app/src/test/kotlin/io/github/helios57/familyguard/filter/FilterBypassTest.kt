package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-26.4: traffic that bypasses the ad filter because it is guaranteed to carry no ads — so the
 * filter spends nothing on it. "Guaranteed" is the whole rule: a package is on the list by name, or
 * it is the phone's own dialer or SMS app AND shipped with the phone. A third-party SMS app can be
 * ad-supported, so being the default is not enough on its own.
 */
class FilterBypassTest {

    private val installed = setOf(
        "com.google.android.gms", "org.thoughtcrime.securesms", "ch.threema.app", "com.sec.imsservice",
        "com.samsung.android.dialer", "com.samsung.android.messaging", "com.textra", "com.whatsapp",
    )
    private val system = setOf("com.google.android.gms", "com.sec.imsservice", "com.samsung.android.dialer", "com.samsung.android.messaging")

    private fun bypass(dialer: String?, sms: String?) = FilterBypass.packages(
        isInstalled = { it in installed },
        isSystem = { it in system },
        defaultDialer = dialer,
        defaultSms = sms,
    )

    @Test
    fun `the named ad-free packages bypass the filter when they are installed`() {
        val got = bypass(null, null)
        for (p in listOf("com.google.android.gms", "org.thoughtcrime.securesms", "ch.threema.app", "com.sec.imsservice")) {
            assertTrue("$p is installed and ad-free and does not bypass: $got", p in got)
        }
        assertFalse("Threema Libre is not installed; excluding it would throw: $got", "ch.threema.app.libre" in got)
    }

    @Test
    fun `WhatsApp never bypasses — it shows ads in Status`() {
        assertFalse("com.whatsapp" in bypass("com.whatsapp", "com.whatsapp"))
    }

    @Test
    fun `the default dialer and SMS app bypass only when they shipped with the phone`() {
        val shipped = bypass("com.samsung.android.dialer", "com.samsung.android.messaging")
        assertTrue("com.samsung.android.dialer" in shipped)
        assertTrue("com.samsung.android.messaging" in shipped)
        val thirdParty = bypass("com.samsung.android.dialer", "com.textra")
        assertFalse("a third-party SMS app may carry ads and bypassed the filter: $thirdParty", "com.textra" in thirdParty)
    }

    @Test
    fun `each package appears once`() {
        val got = bypass("com.google.android.gms", "com.google.android.gms")
        assertEquals(got.distinct(), got)
    }
}
