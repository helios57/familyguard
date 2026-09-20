package io.github.helios57.familyguard.filter

/**
 * The compiled filter: 180k-odd domain suffix rules, answered in the time it takes to split a name.
 *
 * A reverse-label trie rather than a hash set, because every rule in an AdGuard hostlist is a
 * *suffix* rule — `||doubleclick.net^` blocks `doubleclick.net` and everything under it — and a hash
 * set answers only the exact-name question. A set would need one lookup per suffix of the queried
 * name, which is the same walk with worse locality and no way to compare specificity.
 *
 * Specificity is the part that has to be right. Both a block and an allow can match one name, and
 * **the deeper match wins; on a tie the allow wins**. That gives the two cases a list author
 * actually writes:
 *
 *  - block `ads.example.com`, allow `example.com` → `ads.example.com` is **blocked** (3 beats 2)
 *  - block `example.com`, allow `cdn.example.com` → `cdn.example.com` is **allowed** (3 beats 2)
 *
 * The naive rule "any allow wins" gets the first case wrong, and it fails open: the filter quietly
 * stops blocking the exact host the list named. The naive rule "any block wins" gets the second
 * wrong and breaks an app. Neither failure announces itself, which is why the comparison is the
 * thing this class is built around rather than an afterthought in the caller.
 */
class DomainIndex private constructor(private val root: Node, val ruleCount: Int) {

    private class Node {
        val children = HashMap<String, Node>()
        var block = false
        var allow = false
    }

    /**
     * Whether [host] should be blocked.
     *
     * Returns false for anything that is not a domain, including the empty string. A name that
     * cannot be parsed is not a name this filter has an opinion about, and inventing one would mean
     * dropping traffic on the basis of a parse failure.
     */
    fun isBlocked(host: String): Boolean = match(host)?.allow == false

    /**
     * The winning rule for [host], or null if no rule matched.
     *
     * Exposed separately from [isBlocked] so the service can log *why* — "allowed by a rule" and
     * "no rule matched" are the same decision and completely different facts when a parent asks why
     * an app still shows ads.
     */
    fun match(host: String): RuleMatch? {
        val name = RuleParser.normalise(host) ?: return null
        val labels = name.split('.')
        var node = root
        var deepestBlock = -1
        var deepestAllow = -1
        // Walked from the right, so the first step is the TLD and each step narrows. Depth is
        // counted in labels matched, which is exactly the specificity the comparison needs.
        for (i in labels.indices.reversed()) {
            node = node.children[labels[i]] ?: break
            val depth = labels.size - i
            if (node.block) deepestBlock = depth
            if (node.allow) deepestAllow = depth
        }
        if (deepestBlock < 0 && deepestAllow < 0) return null
        return if (deepestAllow >= deepestBlock) {
            RuleMatch(allow = true, labels = deepestAllow)
        } else {
            RuleMatch(allow = false, labels = deepestBlock)
        }
    }

    companion object {
        /** An index with no rules in it. Matches nothing, blocks nothing. */
        val EMPTY: DomainIndex = DomainIndex(Node(), 0)

        fun of(rules: Collection<FilterRule>): DomainIndex {
            val root = Node()
            var count = 0
            for (rule in rules) {
                val labels = rule.domain.split('.')
                var node = root
                for (i in labels.indices.reversed()) {
                    node = node.children.getOrPut(labels[i]) { Node() }
                }
                // A domain can carry both marks — a list may block it and a later list allow it —
                // and keeping both is what lets the specificity comparison stay meaningful instead
                // of depending on which file was read last.
                if (rule.allow) node.allow = true else node.block = true
                count++
            }
            return DomainIndex(root, count)
        }
    }
}

/** What compiling a list produced, including everything it refused and why. */
data class CompileReport(
    val rules: Int,
    val allowRules: Int,
    val skipped: Map<SkipReason, Int>,
) {
    val skippedTotal: Int get() = skipped.values.sum()

    override fun toString(): String =
        "rules=$rules allow=$allowRules skipped=$skippedTotal ${skipped.toSortedMap()}"
}

/** Compiles whole lists into one index, keeping the refusal counts. */
object FilterCompiler {

    fun compile(lines: Sequence<String>): Pair<DomainIndex, CompileReport> {
        val rules = LinkedHashMap<String, FilterRule>()
        val skipped = HashMap<SkipReason, Int>()
        var allowCount = 0
        for (line in lines) {
            when (val result = RuleParser.parse(line)) {
                is ParseResult.Skipped -> skipped[result.reason] = (skipped[result.reason] ?: 0) + 1
                is ParseResult.Rule -> {
                    val rule = result.rule
                    // Keyed by domain *and* side, so a domain that is both blocked and allowed
                    // keeps both marks. De-duplicating by domain alone would let whichever line
                    // came second silently delete the other, and the lists genuinely contain both.
                    val key = "${rule.domain}|${rule.allow}"
                    if (rules.put(key, rule) == null && rule.allow) allowCount++
                }
            }
        }
        val index = DomainIndex.of(rules.values)
        return index to CompileReport(index.ruleCount, allowCount, skipped)
    }
}
