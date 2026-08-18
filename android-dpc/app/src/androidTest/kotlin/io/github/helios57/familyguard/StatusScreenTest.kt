package io.github.helios57.familyguard

import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.enroll.Credentials
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.recovery.RecoveryActivity
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The one screen this app has, rendered on a real device (FR-12, FR-13.4).
 *
 * Everything else about the status block is asserted on the JVM, where `deviceStatus` is a pure
 * function and a case costs a millisecond. What no JVM test can reach is whether any of it appears:
 * a row inflated into a container that was never found, a label bound to the wrong view, a value
 * that lays out zero pixels wide, and a screen that throws while opening the keystore all leave
 * `DeviceStatusTest` completely green — and the screen blank at the moment somebody needs it.
 *
 * Every check here starts by proving it looked at something. A view walk that resolves nothing finds
 * no violations, which is byte-identical to a clean screen.
 *
 * The method names are camel case rather than the backticked sentences the JVM suites use: this app
 * targets `minSdk 29`, and DEX below version 040 refuses a `SimpleName` containing a space. It is a
 * build failure, not a warning, so there is no way to get it wrong quietly.
 */
@RunWith(AndroidJUnit4::class)
class StatusScreenTest {

    /**
     * Waits for the status block to be filled, then hands the view tree to [check].
     *
     * The block is gathered off the main thread — three encrypted files and the usage-stats service
     * — so a test that asserted immediately after `launch` would assert on the loading state and
     * pass or fail on timing. Polling with a deadline is the honest version: it either sees rows or
     * says it never did, and never quietly measures the empty container.
     */
    private fun onRenderedScreen(check: (View) -> Unit) {
        ActivityScenario.launch(RecoveryActivity::class.java).use { scenario ->
            val deadline = System.currentTimeMillis() + RENDER_BUDGET_MILLIS
            var rows = 0
            while (System.currentTimeMillis() < deadline && rows == 0) {
                scenario.onActivity { activity ->
                    rows = activity.findViewById<LinearLayout>(R.id.status_lines).childCount
                }
                if (rows == 0) Thread.sleep(100)
            }
            assertTrue(
                "the status block was still empty after ${RENDER_BUDGET_MILLIS} ms; every " +
                    "assertion below would have been made against a screen with nothing on it",
                rows > 0,
            )
            scenario.onActivity { activity ->
                check(activity.findViewById(android.R.id.content))
            }
        }
    }

    @Test
    fun theStatusBlockRendersALabelledNonEmptyRowForEveryFact() {
        onRenderedScreen { root ->
            val rows = root.findViewById<LinearLayout>(R.id.status_lines)
            assertTrue(
                "the screen shows ${rows.childCount} status rows; the composer emits at least six " +
                    "on any device, so this is a screen that lost some of them on the way to the view",
                rows.childCount >= 6,
            )
            for (i in 0 until rows.childCount) {
                val row = rows.getChildAt(i)
                val label = row.findViewById<TextView>(R.id.status_row_label).text?.toString().orEmpty()
                val value = row.findViewById<TextView>(R.id.status_row_value).text?.toString().orEmpty()
                assertTrue("row $i has no label", label.isNotBlank())
                assertTrue("row $i (\"$label\") has no value", value.isNotBlank())
                assertTrue(
                    "row $i (\"$label\") laid out with no height, so it is on the screen and invisible",
                    row.height > 0,
                )
                assertTrue(
                    "row $i (\"$label\") has no spoken description, so a screen reader reads two " +
                        "unrelated fragments",
                    !row.contentDescription.isNullOrBlank(),
                )
            }
        }
    }

    /**
     * **No text anywhere on this screen is the device token.**
     *
     * `RecoveryActivity` is the launcher entry, exported with no permission, so this screen is
     * readable by whoever is holding the phone. The token authenticates as this device: anyone who
     * read it off the screen could pull the family's policy and report fabricated usage.
     *
     * Asserted against the *rendered tree*, which is the half neither of the other two guards can
     * reach — `DeviceStatusTest` checks the composer's output and `ManifestAndPlatformCallsTest`
     * checks that the three source files never name the field. A token arriving through a fourth
     * file, or through a layout's `android:text`, would pass both of those and fail this.
     */
    @Test
    fun noTextOnTheScreenIsTheDeviceToken() {
        val store = EncryptedCredentialStore(
            InstrumentationRegistry.getInstrumentation().targetContext
        )
        // Enrolled on purpose, rather than measuring whatever this device happens to be. The earlier
        // version of this test returned early when there was no credential, which on an un-enrolled
        // device is a pass that asserted nothing: a phone with no token cannot leak one. That is the
        // shape of green this whole suite exists to refuse, and it would have been the *usual*
        // result — CI devices and fresh emulators are never enrolled.
        val existing = store.load()
        store.save(
            Credentials(
                serverUrl = "https://mdm.invalid.test",
                deviceToken = PROBE_TOKEN,
                deviceId = PROBE_DEVICE_ID,
                childId = "probe-child",
            )
        )
        try {
            onRenderedScreen { root ->
                val texts = textsIn(root)
                // The walk has to be shown to see text at all, or an empty result reads as a clean one.
                assertTrue(
                    "the view walk found no text at all, so it proves nothing about what is on screen",
                    texts.any { it.contains("This phone") },
                )
                assertTrue(
                    "the seeded enrollment did not reach the screen, so the token check below is " +
                        "being made against a screen that shows no credential at all",
                    texts.any { it.contains(PROBE_DEVICE_ID) },
                )
                assertFalse(
                    "the device token is rendered on the launcher screen, which anyone holding the " +
                        "phone can read",
                    texts.any { it.contains(PROBE_TOKEN) },
                )
            }
        } finally {
            // Put the device back. A test that left a fake credential behind would point a real
            // phone at a server that does not exist, and the next run would measure that instead.
            if (existing != null) store.save(existing) else store.clear()
        }
    }

    /**
     * Every control a finger has to hit is at least 48 dp, at the font scale the device is set to.
     *
     * The layout declares `minHeight`, which is a claim about the XML; this is the measurement. A
     * parent using this screen is holding somebody else's phone in one hand, and the target that
     * matters most — the button that unlocks it — is the one at the bottom of a scrolling column
     * where a wrong `layout_height` shows up as a 24 dp strip.
     */
    @Test
    fun theControlsAreAtLeastAFingertipTall() {
        onRenderedScreen { root ->
            val density = root.resources.displayMetrics.density
            val minimum = (48 * density).toInt()
            for (id in listOf(R.id.recovery_code, R.id.recovery_submit)) {
                val view = root.findViewById<View>(id)
                assertTrue(
                    "${root.resources.getResourceEntryName(id)} is ${view.height} px tall; " +
                        "48 dp is $minimum px on this device",
                    view.height >= minimum,
                )
            }
        }
    }

    /**
     * Nothing on this screen is wider than the screen.
     *
     * A horizontal scroll on a phone is not a layout nuisance here: the column contains sentences a
     * parent has to read under pressure, and text that runs off the right edge on a narrow handset
     * is text nobody reads. Measured against the content view's own width rather than the display's,
     * so it holds under a system bar inset and in split screen.
     */
    @Test
    fun nothingIsWiderThanTheScreen() {
        onRenderedScreen { root ->
            val width = root.width
            assertTrue("the content view measured no width at all", width > 0)
            val offenders = mutableListOf<String>()
            walk(root) { view ->
                val location = IntArray(2).also(view::getLocationInWindow)
                if (view.width > 0 && location[0] + view.width > width) {
                    offenders += "${describe(view)} ends at ${location[0] + view.width} px of $width"
                }
            }
            assertTrue("content runs off the right edge: $offenders", offenders.isEmpty())
        }
    }

    private fun textsIn(root: View): List<String> {
        val out = mutableListOf<String>()
        walk(root) { view ->
            if (view is TextView) view.text?.toString()?.takeIf { it.isNotBlank() }?.let(out::add)
            view.contentDescription?.toString()?.takeIf { it.isNotBlank() }?.let(out::add)
        }
        return out
    }

    private fun walk(view: View, visit: (View) -> Unit) {
        visit(view)
        if (view is ViewGroup) for (i in 0 until view.childCount) walk(view.getChildAt(i), visit)
    }

    private fun describe(view: View): String =
        runCatching { view.resources.getResourceEntryName(view.id) }.getOrNull()
            ?: view.javaClass.simpleName

    private companion object {
        /**
         * Long enough for a cold keystore open on a slow device, short enough that a screen which
         * never renders fails instead of hanging the run.
         */
        /**
         * A credential that exists only for the duration of one test.
         *
         * The token is deliberately shaped like a real one — the guard searches for the literal, so
         * a placeholder that no screen could plausibly render would make the search vacuous.
         */
        const val PROBE_TOKEN = "fgt_probeTokenThatMustNeverBeRendered"
        const val PROBE_DEVICE_ID = "fg-probe-device"

        const val RENDER_BUDGET_MILLIS = 15_000L
    }
}
