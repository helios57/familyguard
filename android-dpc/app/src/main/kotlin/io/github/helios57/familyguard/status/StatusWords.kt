package io.github.helios57.familyguard.status

import java.util.Locale

/**
 * Every phrase the status block is made of, by key, in one language.
 *
 * The composer stays pure — no `Context`, no `R` — and still speaks the family's language: on the
 * phone these come from the string resources (`status_w_<key>` in `values` and `values-de`, read by
 * `statusWords(context)`), on the JVM from [ENGLISH]. `StatusWordsTest` holds the three together:
 * [ENGLISH] must equal `values/strings.xml` word for word, `values-de` must carry every key, and the
 * resource map must name every key — so a phrase added here and nowhere else is a red test, not an
 * English sentence on a German phone, which is what this block was until 0.6.34.
 *
 * Formats are Android's positional ones (`%1$s`, `%1$d`), formatted with [Locale.ROOT] so a number
 * is never written with another script's digits.
 */
class StatusWords(private val words: Map<String, String>) {

    operator fun get(key: String, vararg args: Any?): String {
        val format = words[key] ?: error("no status word \"$key\"")
        return if (args.isEmpty()) format else String.format(Locale.ROOT, format, *args)
    }

    /** The label shown for a fact named by one of [StatusLabels]. */
    fun label(fact: String): String = get(LABEL_KEYS[fact] ?: error("no label for the fact \"$fact\""))

    val keys: Set<String> get() = words.keys

    companion object {
        private val LABEL_KEYS = mapOf(
            StatusLabels.ENROLLMENT to "label_enrollment",
            StatusLabels.DEVICE_OWNER to "label_management",
            StatusLabels.BACKGROUND to "label_background",
            StatusLabels.EXACT_ALARMS to "label_alarms",
            StatusLabels.RULES to "label_rules",
            StatusLabels.POLICY to "label_policy",
            StatusLabels.LAST_CONTACT to "label_contact",
            StatusLabels.SCREEN_TIME to "label_screen_time",
            StatusLabels.UNREPORTED to "label_unreported",
        )

        val ENGLISH = StatusWords(
            mapOf(
            "label_enrollment" to "Enrollment",
            "label_management" to "Management",
            "label_background" to "Background activity",
            "label_alarms" to "Alarms",
            "label_rules" to "Rules",
            "label_policy" to "Settings version",
            "label_contact" to "Last reached the family settings",
            "label_screen_time" to "Screen time today",
            "label_unreported" to "Waiting to be reported",
            "enroll_none" to "not set up yet — scan the QR code from the family settings",
            "enroll_as" to "set up as %1${"$"}s",
            "enroll_as_with" to "set up as %1${"$"}s with %2${"$"}s",
            "owner_unknown" to "could not be determined on this phone",
            "owner_no" to "this app is not this phone's device owner, so no rule can be applied",
            "owner_yes" to "this app manages this phone",
            "background_unknown" to "could not be read on this phone",
            "background_restricted" to "restricted, so every wake-up this app books is delayed — up to eight minutes on this phone. Tap the button and choose Allow. On a Samsung, also check Device care → Battery for a sleeping-apps list this app cannot read.",
            "allowed" to "allowed",
            "alarms_unknown" to "could not be read on this phone",
            "alarms_restricted" to "not allowed, so wake-ups are approximate and a bedtime can start late",
            "rules_off" to "off since %1${"$"}s — a recovery code was used. They come back when this phone next reaches the family settings.",
            "rules_on" to "on",
            "policy_none" to "no settings have reached this phone yet",
            "policy_behind" to "version %1${"$"}d was sent, but this phone is still applying version %2${"$"}d",
            "policy_ok" to "version %1${"$"}d",
            "contact_never" to "never",
            "screen_of" to "%1${"$"}d of %2${"$"}d minutes",
            "screen_only" to "%1${"$"}d minutes",
            "screen_no_access" to "not measured: usage access is off, so no daily limit is ever reached. Tap the button and switch FamilyGuard on.",
            "screen_not_measured" to "%1${"$"}s",
            "unreported_one" to "%1${"$"}d recovery attempt this phone has not been able to report yet",
            "unreported_other" to "%1${"$"}d recovery attempts this phone has not been able to report yet",
            "ago_now" to "just now",
            "ago_minute_one" to "%1${"$"}d minute ago",
            "ago_minute_other" to "%1${"$"}d minutes ago",
            "ago_hour_one" to "%1${"$"}d hour ago",
            "ago_hour_other" to "%1${"$"}d hours ago",
            "ago_day_one" to "%1${"$"}d day ago",
            "ago_day_other" to "%1${"$"}d days ago",
            ),
        )
    }
}
