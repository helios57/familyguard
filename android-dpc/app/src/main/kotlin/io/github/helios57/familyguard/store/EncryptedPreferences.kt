package io.github.helios57.familyguard.store

import android.content.Context
import android.content.SharedPreferences
import android.util.Log

private const val TAG = "EncryptedPreferences"

/**
 * Preferences whose values are sealed under a keystore-held key, opened the same way everywhere.
 *
 * Two things are stored on this device that a rooted phone should not give up as a file to copy:
 * the device credential, and the last policy the server sent. The second is less obviously
 * sensitive and is not — it is a child's app list, schedule and blocked domains, which is a fair
 * description of what that child does all day.
 *
 * One function rather than four setups, because the interesting part is the *scheme*, and four
 * copies of it drift: the day one of them is opened with a weaker one, nothing fails and nothing
 * looks different.
 *
 * [file] is the preferences file name; each caller keeps its own so that clearing one — an
 * un-enrollment dropping the credential — cannot take the other with it. The file name is also
 * bound into every value it holds, so a blob cannot be moved between these files.
 *
 * ### It used to be `EncryptedSharedPreferences`
 *
 * `androidx.security:security-crypto` reached 1.1.0 stable with `MasterKey`,
 * `EncryptedSharedPreferences` and every nested scheme deprecated — the whole library, not a corner
 * of it. Keeping a bearer credential in a crypto library that will get no further fixes is not a
 * thing to defer, so the three pieces it provided are now [KeystoreSecretCipher] (the key and the
 * AES-256-GCM), [CipherPreferences] (the `SharedPreferences` shape and the typed encoding) and this
 * function. The upgrade to that stable version is what surfaced it: `allWarningsAsErrors` turned
 * eleven deprecation warnings into a failed build.
 *
 * Nothing has to be migrated. No version of this app has been released, so there is no device
 * holding a file in the old format. On a *development* device that ran an earlier build, the old
 * file is still there, none of its entries are readable under the new scheme, and every store above
 * reads that as "absent" — so the phone enrolls again through the enrollment screen. Its device
 * owner status is untouched by any of that; nothing here needs a factory reset.
 */
fun encryptedPreferences(context: Context, file: String): SharedPreferences =
    CipherPreferences(
        file = file,
        delegate = context.applicationContext.getSharedPreferences(file, Context.MODE_PRIVATE),
        cipher = KeystoreSecretCipher(report = { Log.e(TAG, it) }),
        // Never `Log.d`. A value that will not open is either a real fault or a tampered file, and
        // both are things someone reading a bug report needs to see without a filter set.
        onUnreadable = { Log.e(TAG, it) },
    )
