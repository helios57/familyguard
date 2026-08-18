package io.github.helios57.familyguard

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Every link between the documents in this repository resolves — to a file that exists, and, where
 * the link names a section, to a heading that exists in it.
 *
 * The eight Markdown documents cross-reference each other about fifty times, and until this class
 * existed nothing checked one of them. A link to a renamed file and a link to a renamed heading fail
 * the same way GitHub fails them: silently. The reader clicks, lands at the top of the page or on a
 * 404, and concludes the document is stale — which is the opposite of what a heavily cross-linked
 * doc set is for. It is the same shape as [RequirementCitationsTest]: a reference that looks like
 * evidence and points at nothing.
 *
 * **The anchor rule is the part worth getting right, and the naive version is wrong.** GitHub
 * lowercases a heading, drops everything that is not a letter, digit, space, `_` or `-`, and then
 * maps **each remaining space to its own hyphen** — it does not collapse runs. So `## 6.6 — the row`
 * becomes `#66--the-row`, with the double hyphen left behind by the em dash. A checker that collapses
 * whitespace reports every such link dead; the first draft of this guard did exactly that and
 * produced six false deads out of twelve anchored links, which is worse than no guard — a checker
 * that cries wolf gets its findings edited away rather than its bug fixed. [slug] is pinned against
 * that case below.
 *
 * Fenced code blocks are stripped before both halves of the scan. README's build section contains
 * the shell comment `# control plane` inside a fence; read as a heading it would invent an anchor
 * that resolves and hide a link that should have been reported.
 *
 * External links are not fetched. A unit test that reaches the network is a test that fails when the
 * network does, and this suite's whole claim is that a red means the repository is wrong. NFR-12.
 */
class DocumentationLinksTest {

    private val root: File = generateSequence(File(".").absoluteFile) { it.parentFile }
        .firstOrNull { File(it, "REQUIREMENTS.md").isFile }
        ?: throw AssertionError(
            "could not find REQUIREMENTS.md above ${File(".").absolutePath}; this test would " +
                "otherwise pass by finding no documents and checking no links",
        )

    private val docs: List<File> = root.walkTopDown().onEnter { it.name !in SKIPPED_DIRS }
        .filter { it.isFile && it.extension == "md" }
        .sortedBy { it.path }
        .toList()

    /** Every heading anchor in [file], by GitHub's rule. */
    private fun anchors(file: File): Set<String> =
        HEADING.findAll(FENCE.replace(file.readText(), ""))
            .map { slug(it.groupValues[1]) }
            .toSet()

    private data class Link(val from: File, val line: Int, val target: String)

    private val links: List<Link> = docs.flatMap { file ->
        // Each fence collapses to the newlines it contained, so the reported line number is the one
        // the reader will open. A guard that names the wrong line costs more than it saves.
        val text = FENCE.replace(file.readText()) { m -> "\n".repeat(m.value.count { it == '\n' }) }
        LINK.findAll(text).map { m ->
            Link(file, text.take(m.range.first).count { it == '\n' } + 1, m.groupValues[2])
        }
    }.filterNot { it.target.startsWith("http://") || it.target.startsWith("https://") || it.target.startsWith("mailto:") }

    @Test
    fun `the scan read the documents, and the anchor rule is GitHub's`() {
        // First, because every assertion below is "this list of broken links is empty" and an empty
        // list is also what a scan that read nothing produces.
        assertTrue(
            "found only ${docs.size} Markdown documents under $root; the walk is reading the wrong " +
                "directory and every link check below is vacuous",
            docs.size >= 8,
        )
        assertTrue(
            "the scan collected only ${links.size} internal links, which is fewer than this doc set " +
                "has ever had; the link pattern has stopped matching",
            links.size >= 40,
        )
        assertTrue(
            "the scan collected only ${links.count { "#" in it.target }} anchored links, so the " +
                "half of this guard that checks section names is not being exercised at all",
            links.count { "#" in it.target } >= 8,
        )

        // The rule itself, pinned on the case the obvious implementation gets wrong. If this is ever
        // "fixed" to collapse the run, every em-dash heading in IMPLEMENTATION_PLAN.md goes red.
        assertEquals(
            "GitHub maps each space to its own hyphen and does not collapse runs; a slug that " +
                "collapses them reports every `## N — title` link dead",
            "66--the-row-that-passed-94-of-the-time",
            slug("6.6 — the row that passed 94% of the time"),
        )
        assertEquals("backticks and apostrophes are dropped, not replaced", "run_allshs-layers", slug("`run_all.sh`'s layers"))

        // Fences are stripped from the heading scan. Without this the shell comment below is an
        // anchor, and a link to `#control-plane` resolves against a heading nobody wrote.
        val fenced = "## Real heading\n\n```bash\n# control plane\ngo build ./...\n```\n"
        assertEquals(
            "a `#` line inside a fenced code block is a comment, not a heading",
            setOf("real-heading"),
            HEADING.findAll(FENCE.replace(fenced, "")).map { slug(it.groupValues[1]) }.toSet(),
        )

        // The authority must be able to say no, in both directions.
        // The positive control is pinned to a heading README *links to itself*, deliberately: if
        // someone renames it, the link check below goes red for the real reason, and this pin is not
        // the only thing standing between a rename and a green suite.
        val readme = File(root, "README.md")
        assertTrue("README.md has a Deployment section and the scanner did not see it", "deployment" in anchors(readme))
        assertFalse(
            "README.md has no such heading, but the scanner accepted the anchor, so it accepts anything",
            "there-is-no-heading-with-this-name" in anchors(readme),
        )
    }

    @Test
    fun `every link points at a file that exists`() {
        val broken = links.filter { it.target.substringBefore('#').isNotEmpty() }
            .filterNot { File(it.from.parentFile, it.target.substringBefore('#')).exists() }
        assertEquals(
            "these links name a path that is not in the repository, so following one gets a 404: " +
                broken.joinToString("; ") { "${it.from.relativeTo(root).path}:${it.line} -> ${it.target}" },
            emptyList<Link>(), broken,
        )
    }

    @Test
    fun `every link to a section points at a heading that exists`() {
        val dead = links.filter { "#" in it.target }.filterNot { link ->
            val (path, anchor) = link.target.substringBefore('#') to link.target.substringAfter('#')
            val target = if (path.isEmpty()) link.from else File(link.from.parentFile, path)
            // Anchors are only meaningful in a document this scan can read the headings of. A `#L42`
            // on a source file is GitHub's line syntax and is none of this guard's business.
            if (target.extension != "md" || !target.isFile) true else anchor in anchors(target)
        }
        assertEquals(
            "these links name a section that does not exist; GitHub answers one by silently landing " +
                "the reader at the top of the page, which reads as a stale document: " +
                dead.joinToString("; ") { "${it.from.relativeTo(root).path}:${it.line} -> ${it.target}" },
            emptyList<Link>(), dead,
        )
    }

    private companion object {
        val FENCE = Regex("```.*?```", RegexOption.DOT_MATCHES_ALL)
        val HEADING = Regex("^#{1,6}\\s+(.*?)\\s*$", RegexOption.MULTILINE)

        /** `[text](target)`, and `![alt](src)` too — an image that 404s is also a broken link. */
        val LINK = Regex("""\[([^\]]*)\]\(([^)\s]+)\)""")

        val SKIPPED_DIRS = setOf("build", ".git", ".gradle", ".idea", ".kotlin", "node_modules", "scratchpad")

        /** GitHub's heading-anchor rule. See this class's header for why the naive version is wrong. */
        fun slug(heading: String): String = heading.trim().lowercase()
            .replace(Regex("""[^\p{L}\p{N}\s_-]"""), "")
            .replace(' ', '-')
    }
}
