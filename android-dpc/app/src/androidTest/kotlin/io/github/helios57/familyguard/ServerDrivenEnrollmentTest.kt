package io.github.helios57.familyguard

import android.os.PersistableBundle
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.enroll.Credentials
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.enroll.Enroller
import io.github.helios57.familyguard.sync.ConnectionService
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Enrolls this device against the control plane named in the instrumentation arguments.
 *
 * **This is one half of a two-process test, and it is not run by the ordinary instrumented sweep.**
 * The other half is `TestTheServerReplacesTheDPCOnARealDevice` in `tests/e2e`, which holds the
 * server, mints the enrollment token this class is handed, and afterwards asserts what the phone
 * did with it. `tests/android/self-update.sh` runs the two together; `tests/android/instrumented.sh`
 * excludes this class by name.
 *
 * Enrollment cannot be driven from `adb` on an emulator, which is why this class exists at all. The
 * real path is a provisioning QR: the setup wizard hands `PROVISIONING_ADMIN_EXTRAS_BUNDLE` to
 * `PolicyComplianceActivity`, which passes it to [ConnectionService.start]. On a booted emulator
 * there is no setup wizard to run, and `PROFILE_PROVISIONING_COMPLETE` is a protected broadcast that
 * `adb shell am broadcast` may not send. An instrumentation runs inside this app's own UID, so it
 * can make **the same call the provisioning activity makes**, with the same extras — which is the
 * point: this class must not be a second, easier enrollment path that works when the real one would
 * not.
 *
 * It FAILS rather than skips when the arguments are absent. A skip here would be a green in the one
 * layer that can see the device, and the reason names the script that is supposed to be driving it.
 */
@RunWith(AndroidJUnit4::class)
class ServerDrivenEnrollmentTest {

    @Test
    fun enrollsAgainstTheServerNamedInTheArguments() {
        val args = InstrumentationRegistry.getArguments()
        val serverUrl = args.getString(ARG_SERVER_URL).orEmpty()
        val token = args.getString(ARG_ENROLLMENT_TOKEN).orEmpty()
        assertTrue(
            "no -e $ARG_SERVER_URL argument. This class is the device half of the FR-15 " +
                "self-update proof and is driven by tests/android/self-update.sh; running it on " +
                "its own has nothing to enroll against.",
            serverUrl.isNotEmpty(),
        )
        assertTrue(
            "no -e $ARG_ENROLLMENT_TOKEN argument. The token is single-use and is minted by the " +
                "server the e2e harness starts; see tests/android/self-update.sh.",
            token.isNotEmpty(),
        )

        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val store = EncryptedCredentialStore(context)

        // Enroller answers AlreadyEnrolled to a device that holds a credential, before it reads the
        // extras at all — which is correct for the product and fatal here: a credential left by an
        // earlier run would make this method pass without a single byte crossing the network, and
        // the phone would then be pointed at a server that stopped existing when that run ended.
        store.clear()
        assertNull(
            "the credential store still holds a credential after clear(); everything below would " +
                "measure the previous run",
            store.load(),
        )

        ConnectionService.start(
            context,
            PersistableBundle().apply {
                putString(Enroller.EXTRA_SERVER_URL, serverUrl)
                putString(Enroller.EXTRA_ENROLLMENT_TOKEN, token)
            },
        )

        val credentials = awaitCredentials(store)
        assertTrue(
            "the device did not enrol within ${ENROLL_BUDGET_MILLIS / 1000}s. The service logs " +
                "the reason under the FamilyGuard tag; a refused token and an unreachable server " +
                "look the same from here.",
            credentials != null,
        )
        credentials!!
        assertEquals(
            "the device enrolled against a different server than it was told to",
            serverUrl,
            credentials.serverUrl,
        )
        assertTrue(
            "the device stored a credential with no device token, which it cannot authenticate with",
            credentials.deviceToken.isNotEmpty(),
        )
        assertTrue(
            "the device stored a credential with no device id",
            credentials.deviceId.isNotEmpty(),
        )
    }

    /**
     * Polls until the credential lands, or the budget runs out.
     *
     * A single read after a fixed sleep is the version of this that passes on a fast machine and
     * fails on a loaded one; enrollment here crosses a real socket, and the service is starting a
     * foreground notification and reading the installed-package list on the way.
     */
    private fun awaitCredentials(store: EncryptedCredentialStore): Credentials? {
        val deadline = System.currentTimeMillis() + ENROLL_BUDGET_MILLIS
        while (System.currentTimeMillis() < deadline) {
            store.load()?.let { return it }
            Thread.sleep(POLL_MILLIS)
        }
        return store.load()
    }

    private companion object {
        const val ARG_SERVER_URL = "familyguardServerUrl"
        const val ARG_ENROLLMENT_TOKEN = "familyguardEnrollmentToken"

        /**
         * 90 s. The exchange itself is one request over loopback, but everything before it is the
         * service coming up: `startForeground`, the keystore, and `DeviceFacts` walking the
         * installed packages. Measured at a few seconds on this emulator; the budget is wide because
         * the cost of it being too tight is a red that reads as a broken product.
         */
        const val ENROLL_BUDGET_MILLIS = 90_000L
        const val POLL_MILLIS = 250L
    }
}
