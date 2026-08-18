package io.github.helios57.familyguard

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Every `FR-…` and `NFR-…` in this repository resolves to a requirement, and every requirement is
 * claimed by something.
 *
 * The traceability table in IMPLEMENTATION_PLAN.md is written by hand, and so is every citation in
 * every comment. Nothing checked either of them until this class existed, and four were wrong: three
 * Go comments cited `FR-14.1`, `FR-4.4` and `FR-9.4`, none of which exist — the real ids are FR-14,
 * FR-4.2 and FR-9.2 — and `FR-13.4` was cited in twelve places by the code that implements it while
 * REQUIREMENTS.md stopped at FR-13.3.
 *
 * Neither mistake has a symptom. A citation pointing at a number nobody wrote reads exactly like one
 * pointing at the right requirement, and it is *worse* than no citation: it is what a reviewer
 * follows to decide whether the code does what was asked, and it leads them to a requirement that
 * says something else. That is the same defect as a guard that never runs — a control that looks
 * like evidence and evaluates nothing.
 *
 * This runs on the JVM, in the Android unit layer, and scans the whole repository including the Go
 * backend. That is deliberate: the citations do not respect language boundaries, so a guard that did
 * would be blind to most of them.
 */
class RequirementCitationsTest {

    /** `FR-3`, `FR-13.4`, `NFR-11` — the two shapes REQUIREMENTS.md uses, and nothing else. */
    private val citation = Regex("""\b(N?FR-\d+(?:\.\d+)?)\b""")

    /**
     * A requirement is *defined* where its id opens a heading or a bullet. Anywhere else in
     * REQUIREMENTS.md it is a cross-reference — FR-7 is named in FR-9's command table, and reading
     * that as a definition would let a typo define itself.
     */
    private val definition = Regex("""^(?:#+\s+|-\s+(?:\*\*)?)(N?FR-\d+(?:\.\d+)?)\b""")

    private val root: File = generateSequence(File(".").absoluteFile) { it.parentFile }
        .firstOrNull { File(it, "REQUIREMENTS.md").isFile }
        ?: throw AssertionError(
            "could not find REQUIREMENTS.md above ${File(".").absolutePath}; this test would " +
                "otherwise pass by comparing two empty sets",
        )

    private val defined: Set<String> = File(root, "REQUIREMENTS.md").readLines()
        .mapNotNull { definition.find(it)?.groupValues?.get(1) }
        .toSet()

    /**
     * Where each id is cited, excluding REQUIREMENTS.md itself — a document cannot corroborate its
     * own numbering.
     *
     * `build/` and friends are skipped because they hold *copies*: `app/build/intermediates` carries
     * the same layout XML as `src/main/res`, so a citation deleted from the source would go on
     * resolving out of a stale build directory until someone ran `clean`.
     *
     * This file is skipped too, and it is the only source file that is: it names the four ids that
     * were wrong, plus a fabricated one, and every one of them is supposed to resolve to nothing.
     * The exclusion is one path rather than a pattern, and the scan asserts below that it still
     * reaches every language — an exclusion that silently widened is how a guard stops guarding.
     */
    private val cited: Map<String, List<String>> = buildMap<String, MutableList<String>> {
        for (file in root.walkTopDown().onEnter { it.name !in SKIPPED_DIRS }) {
            if (!file.isFile || file.extension !in SCANNED_EXTENSIONS) continue
            val relative = file.relativeTo(root).path
            if (relative.endsWith(OWN_SOURCE) || relative == "REQUIREMENTS.md") continue
            citation.findAll(file.readText()).map { it.groupValues[1] }.distinct()
                .forEach { getOrPut(it) { mutableListOf() }.add(relative) }
        }
    }

    @Test
    fun `the scan read the repository, in every language that cites a requirement`() {
        // First, because everything below is a comparison of two sets and both are empty when the
        // walk resolves the wrong directory or the pattern stops matching. This exact guard was
        // written once with a backspace escape instead of a word boundary — a pattern that matches
        // nothing, in a scanner whose entire job is to match — and it reported the repository clean.
        assertTrue(
            "REQUIREMENTS.md yielded only ${defined.size} requirement ids, which is fewer than the " +
                "document has ever had; the definition pattern has stopped matching and every " +
                "citation below would resolve against an empty authority",
            defined.size >= 70,
        )
        assertTrue(
            "the scan found ${cited.size} cited ids, which is too few to be this repository",
            cited.size >= 70,
        )
        val extensions = cited.values.flatten().distinct()
            .mapNotNull { it.substringAfterLast('.', "").takeIf(String::isNotEmpty) }.toSet()
        assertTrue(
            "requirements are cited from Kotlin, Go, XML and Markdown; this scan saw only " +
                "$extensions, so it is reading one part of the repository and reporting on all of it",
            setOf("kt", "go", "xml", "md").all { it in extensions },
        )
        // The authority must be able to say no. Without this, a `defined` set built by some future
        // pattern that matches every line would make the whole class vacuous while staying green.
        assertFalse(
            "FR-97.3 is not a requirement, but the authority accepted it, so it accepts anything",
            "FR-97.3" in defined,
        )
        assertTrue("FR-13.4 is a requirement and the authority did not see it", "FR-13.4" in defined)
        // The one exemption has to still name a file that exists. If this class is renamed and the
        // constant is not, the exemption stops matching and this test starts flagging its own
        // examples — loudly, which is the right direction for an exemption to fail.
        assertTrue(
            "the exempted path $OWN_SOURCE does not exist under $root, so the exclusion no longer " +
                "describes anything and is free to be widened without anyone noticing",
            root.walkTopDown().any { it.isFile && it.path.endsWith(OWN_SOURCE) },
        )
    }

    @Test
    fun `every requirement cited anywhere in this repository exists`() {
        val dangling = cited.filterKeys { it !in defined }
        assertEquals(
            "these ids are cited by code, tests or documents and are not in REQUIREMENTS.md, so " +
                "whoever follows one lands on a requirement that says something else, or on nothing: " +
                dangling.entries.joinToString("; ") { (id, files) ->
                    "$id (${files.take(3).joinToString(", ")})"
                },
            emptySet<String>(), dangling.keys,
        )
    }

    @Test
    fun `every requirement is claimed by something`() {
        val unclaimed = defined - cited.keys
        assertEquals(
            "these requirements are in REQUIREMENTS.md and nothing anywhere names them — either they " +
                "were dropped without being withdrawn, or the thing that implements them cites the " +
                "wrong number: $unclaimed",
            emptySet<String>(), unclaimed,
        )
    }

    private companion object {
        val SCANNED_EXTENSIONS =
            setOf("kt", "kts", "go", "md", "xml", "sh", "py", "ts", "yaml", "yml", "sql")

        /**
         * Build outputs, VCS metadata and tool caches. Named rather than pattern-matched so adding
         * one is a decision somebody made, not a directory that quietly stopped being scanned.
         */
        val SKIPPED_DIRS = setOf("build", ".git", ".gradle", ".idea", ".kotlin", "node_modules", "scratchpad")

        /** This class's own source. See [cited] for why exactly one file is exempt. */
        const val OWN_SOURCE = "io/github/helios57/familyguard/RequirementCitationsTest.kt"
    }
}
