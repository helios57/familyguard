package io.github.helios57.familyguard.device

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The list a phone reports as never-suspend (FR-5.5) and as the home screen (FR-3.8), from what the
 * platform answered. The reads themselves need a phone; what is asserted here is what is made of
 * their answers.
 */
class CriticalPackagesTest {

    @Test
    fun `a Samsung's answers are reported in the order asked`() {
        assertEquals(
            listOf("com.samsung.android.dialer", "com.sec.android.app.launcher", "com.android.settings", "com.samsung.android.messaging", "com.samsung.android.honeyboard"),
            CriticalPackages.reportable(
                listOf("com.samsung.android.dialer", "com.sec.android.app.launcher", "com.android.settings", "com.samsung.android.messaging", "com.samsung.android.honeyboard"),
            ),
        )
    }

    /** No default launcher chosen: HOME resolves to the framework's chooser, which is not a launcher. */
    @Test
    fun `the framework's resolver is not a package the child uses`() {
        assertEquals(
            listOf("com.google.android.dialer", "com.android.settings"),
            CriticalPackages.reportable(listOf("com.google.android.dialer", "android", "com.android.settings")),
        )
        assertEquals(emptyList<String>(), CriticalPackages.reportable(listOf("android")))
    }

    /** A tablet with no telephony has no dialer and no SMS app; that is a correct empty answer. */
    @Test
    fun `a read that found nothing is left out, not reported as a name`() {
        assertEquals(
            listOf("com.android.launcher3", "com.android.settings"),
            CriticalPackages.reportable(listOf(null, "com.android.launcher3", "com.android.settings", null, "", "  ")),
        )
    }

    /** Google's dialer and messages can share a keyboard, and two IMEs can live in one package. */
    @Test
    fun `a package named twice is reported once, where it first appeared`() {
        assertEquals(
            listOf("com.google.android.apps.messaging", "com.google.android.inputmethod.latin"),
            CriticalPackages.reportable(
                listOf("com.google.android.apps.messaging", "com.google.android.inputmethod.latin", "com.google.android.inputmethod.latin", "com.google.android.apps.messaging"),
            ),
        )
    }

    @Test
    fun `nothing read is an empty list`() {
        assertEquals(emptyList<String>(), CriticalPackages.reportable(emptyList()))
    }
}
