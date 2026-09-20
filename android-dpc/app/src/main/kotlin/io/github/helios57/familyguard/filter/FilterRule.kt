package io.github.helios57.familyguard.filter

/**
 * What a rule says about a domain, and how specific it was being when it said it.
 *
 * [labels] is the number of dot-separated labels the rule matched — `ads.example.com` is 3. It is
 * carried because the comparison between a block and an allow is decided by specificity, not by
 * order: a list that blocks `example.com` and allows `cdn.example.com` must allow the CDN, and a
 * list that blocks `ads.example.com` and allows `example.com` must still block the ads. Deciding by
 * "an allow anywhere wins" gets the second case wrong, and gets it wrong in the direction that
 * silently stops blocking.
 */
data class RuleMatch(val allow: Boolean, val labels: Int)

/** A parsed rule: a domain suffix, and whether matching it allows or blocks. */
data class FilterRule(val domain: String, val allow: Boolean)

/**
 * Why a line produced no rule.
 *
 * Refusals are counted and reported rather than dropped, because the interesting number is not how
 * many rules compiled — it is how many did not, and which kind. A list whose refusal profile changes
 * between two fetches has changed shape, and that is worth seeing before it is worth debugging.
 */
enum class SkipReason {
    /** Blank, or a `!` / `#` comment. Expected, and not interesting. */
    COMMENT,

    /** A cosmetic rule (`##`, `#?#`, `#$#`). Nothing outside the page can act on one. */
    COSMETIC,

    /** Carries a `$` modifier. AdGuard's own DNS syntax says such a rule must be ignored entirely. */
    MODIFIER,

    /** A `/regex/` rule. Not supported, and deliberately: an unbounded regex per query is a trap. */
    REGEX,

    /** A partial anchor or substring rule — `||bc.geocities.`, `-iklan1.`, or a scheme-anchored one. */
    PARTIAL,

    /** Parsed to something that is not a usable domain. */
    NOT_A_DOMAIN,
}

/** One line in, either a rule or a reason there wasn't one. */
sealed interface ParseResult {
    data class Rule(val rule: FilterRule) : ParseResult
    data class Skipped(val reason: SkipReason) : ParseResult
}

/**
 * Turns one line of an AdGuard hostlist — or a hosts file, or a bare domain list — into a rule.
 *
 * **The `@@` case is handled first and on purpose.** Measured on AdGuard's own DNS filter
 * (`filter_1.txt`, 181,125 lines, fetched 2026-09-20): 180,606 lines are plain `||domain^`, and
 * **207 are `@@` exceptions**. The obvious compiler — strip `||`, strip `^`, keep the rest — turns
 * every one of those 207 into a *blocking* rule for the exact host the list author wrote down to
 * keep working. That does not under-block, it breaks an app, and it breaks it in a way that looks
 * like the app's own fault. So exceptions are recognised before anything else is stripped.
 *
 * Everything this cannot represent is refused rather than approximated. A `$`-modified rule is not
 * "mostly" a domain rule: AdGuard's DNS filtering syntax says *"If a rule contains a modifier not
 * listed in this document, the whole rule must be ignored"*, and the rules carrying modifiers are
 * disproportionately the narrow ones whose broad form would be wrong.
 */
object RuleParser {

    fun parse(rawLine: String): ParseResult {
        val line = rawLine.trim()
        if (line.isEmpty() || line.startsWith("!") || line.startsWith("#")) {
            return ParseResult.Skipped(SkipReason.COMMENT)
        }

        // Exceptions first — see the class comment. `@@` survives every other transformation below
        // as an ordinary character, so recognising it late means never recognising it at all.
        var body = line
        val allow = body.startsWith("@@")
        if (allow) body = body.removePrefix("@@")

        // A cosmetic rule hides an element inside a page. Nothing at the packet layer can do that,
        // and the separator is checked before `#` comments would have a chance to eat the line.
        if (body.contains("##") || body.contains("#?#") || body.contains("#\$#")) {
            return ParseResult.Skipped(SkipReason.COSMETIC)
        }
        if (body.startsWith("/") && body.lastIndexOf('/') > 0) {
            return ParseResult.Skipped(SkipReason.REGEX)
        }
        if (body.contains('$')) return ParseResult.Skipped(SkipReason.MODIFIER)

        // Hosts format: `0.0.0.0 ads.example.com`, `127.0.0.1 ads.example.com`, `::1 …`. Only the
        // leading address is dropped; a line with more than two fields is a hosts file listing
        // several names for one address, and every name on it is a name the author blocked.
        val fields = body.split(Regex("\\s+")).filter { it.isNotEmpty() }
        if (fields.size > 1 && isBlockingAddress(fields[0])) {
            body = fields[1]
        } else if (fields.size > 1) {
            // Two tokens and the first is not an address we recognise. Guessing which one is the
            // domain is how a rule ends up pointing at something nobody wrote.
            return ParseResult.Skipped(SkipReason.PARTIAL)
        } else {
            body = fields.firstOrNull() ?: return ParseResult.Skipped(SkipReason.NOT_A_DOMAIN)
        }

        val anchored = body.startsWith("||")
        if (anchored) body = body.removePrefix("||")
        body = body.removeSuffix("^")

        // What is left must be a whole domain. Anything still carrying a scheme, a path, a wildcard
        // or an anchor is a substring or partial-anchor rule, and evaluating one at the domain layer
        // means either missing it or over-matching — `||bc.geocities.` would become the domain
        // `bc.geocities`, which blocks nothing, or `bc.geocities.*`, which blocks strangers.
        if (body.contains("://") || body.contains('/') || body.contains('*') || body.contains('|')) {
            return ParseResult.Skipped(SkipReason.PARTIAL)
        }
        if (body.endsWith(".") || body.startsWith(".")) {
            return ParseResult.Skipped(SkipReason.PARTIAL)
        }

        val domain = normalise(body) ?: return ParseResult.Skipped(SkipReason.NOT_A_DOMAIN)
        return ParseResult.Rule(FilterRule(domain, allow))
    }

    /**
     * The addresses a hosts file uses to mean "nowhere".
     *
     * A hosts line pointing at a real address is a redirection, not a block, and treating one as a
     * block would take a name the author deliberately re-pointed and make it unreachable instead.
     */
    private fun isBlockingAddress(token: String): Boolean =
        token == "0.0.0.0" || token == "127.0.0.1" || token == "::1" || token == "::" ||
            token == "::0" || token == "0:0:0:0:0:0:0:0" || token == "0:0:0:0:0:0:0:1"

    /**
     * Lower-cases and validates a domain, returning null for anything that is not one.
     *
     * ASCII only, by design. A list entry in Unicode has already been punycoded by every compiler
     * that produces these files, and a name arriving from the wire is punycoded too — so accepting
     * raw Unicode here would create entries that can never match anything.
     */
    fun normalise(raw: String): String? {
        val host = raw.trim().trim('.').lowercase()
        if (host.isEmpty() || host.length > 253) return null
        if (host.none { it == '.' }) {
            // A single label is either a hostname on the local network or the residue of a mangled
            // rule. Blocking one by suffix would block every name under it, which for `com` is the
            // whole internet.
            return null
        }
        for (label in host.split('.')) {
            if (label.isEmpty() || label.length > 63) return null
            for (ch in label) {
                val ok = (ch in 'a'..'z') || (ch in '0'..'9') || ch == '-' || ch == '_'
                if (!ok) return null
            }
        }
        return host
    }
}
