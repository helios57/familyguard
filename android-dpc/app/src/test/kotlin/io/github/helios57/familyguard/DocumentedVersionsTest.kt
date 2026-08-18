package io.github.helios57.familyguard

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * No document states a version that disagrees with the build file it describes.
 *
 * This exists because that drift had already happened twice and neither time was visible. CONCEPT.md
 * said `targetSdk 34` and `compileSdk 35` months after the build moved to 37, and the README said the
 * bytecode target was 17 for as long as it was 17 and then for as long as it took someone to notice.
 * A stale version in prose is not a cosmetic defect: `Prerequisites:` is the line a new contributor
 * installs from, and a number that is wrong there costs an afternoon before it costs a build.
 *
 * It is the third guard of the same family as [DocumentationLinksTest] and [RequirementCitationsTest]
 * — a claim in a document that looks like it was checked and was not. The difference is the direction
 * of the check: those two ask whether a reference *resolves*, this one asks whether a stated fact is
 * still *true*. The build files are the authority in every case; a doc never wins an argument here.
 *
 * **Two mechanisms, because prose and code do not read the same.**
 *
 *  1. [IDENTIFIERS] — wherever a document writes a build-file identifier (`minSdk`, `jvmTarget`, …)
 *     followed by a value, the value must be the one the build file holds. This is exact and cannot
 *     produce a false red: those identifiers appear in exactly one context. It is also why the docs
 *     were edited to *name* the identifier where they used to state a bare number — "the bytecode
 *     target is 17" is unguardable, "`jvmTarget` 21" is guarded by construction.
 *  2. [CLAIMS] — a registry of the sentences that state a version without naming an identifier, each
 *     pinned by the phrasing it actually uses. **Every entry must match at least once**, and that
 *     requirement is the whole value of the mechanism: a regex that silently stops matching is a
 *     guard that has been switched off, which is the failure mode this repository keeps finding. So
 *     rewording one of these sentences turns the suite red. That is intended, not a nuisance — those
 *     sentences are load-bearing, and the moment to re-check the number is the moment someone is
 *     already editing the line.
 *
 * What this deliberately does **not** do is parse prose for version-shaped numbers in general. The
 * README says AGP "documents JDK 17 as its minimum", which is a floor and true, and a scanner that
 * saw `JDK 17` next to a pinned `JAVA_VERSION` of 26 would report it — a checker that cries wolf gets
 * its findings edited away rather than its bug fixed, and the edits break the sentences that worked.
 */
class DocumentedVersionsTest {

    private val root: File = generateSequence(File(".").absoluteFile) { it.parentFile }
        .firstOrNull { File(it, "REQUIREMENTS.md").isFile }
        ?: throw AssertionError(
            "could not find REQUIREMENTS.md above ${File(".").absolutePath}; this test would " +
                "otherwise pass by reading no documents and checking no versions",
        )

    private val docs: List<File> = root.walkTopDown().onEnter { it.name !in SKIPPED_DIRS }
        .filter { it.isFile && it.extension == "md" }
        .sortedBy { it.path }
        .toList()

    /**
     * The values the build actually uses, read from the files that define them.
     *
     * Comment lines are stripped from the Gradle script first. `build.gradle.kts` explains `minSdk`
     * in a comment that names the 26 it used to be, and an authority that read its own commentary
     * would take whichever number the regex reached first.
     */
    private val authority: Map<String, String> = buildMap {
        val gradle = File(root, "android-dpc/app/build.gradle.kts").readText()
            .lineSequence().filterNot { it.trimStart().startsWith("//") }.joinToString("\n")
        put("compileSdk", capture(gradle, """compileSdk\s*=\s*(\d+)""", "app/build.gradle.kts"))
        put("compileSdkMinor", capture(gradle, """compileSdkMinor\s*=\s*(\d+)""", "app/build.gradle.kts"))
        put("buildToolsVersion", capture(gradle, """buildToolsVersion\s*=\s*"([^"]+)"""", "app/build.gradle.kts"))
        // The README states the platform as one number, because `sdkmanager` does: `android-37.1`.
        put("compileSdkPlatform", "${getValue("compileSdk")}.${getValue("compileSdkMinor")}")
        put("minSdk", capture(gradle, """minSdk\s*=\s*(\d+)""", "app/build.gradle.kts"))
        put("targetSdk", capture(gradle, """targetSdk\s*=\s*(\d+)""", "app/build.gradle.kts"))
        put("versionName", capture(gradle, """versionName\s*=\s*"([^"]+)"""", "app/build.gradle.kts"))
        put("jvmTarget", capture(gradle, """JvmTarget\.JVM_(\d+)""", "app/build.gradle.kts"))
        put("sourceCompatibility", capture(gradle, """sourceCompatibility\s*=\s*JavaVersion\.VERSION_(\d+)""", "app/build.gradle.kts"))
        put("targetCompatibility", capture(gradle, """targetCompatibility\s*=\s*JavaVersion\.VERSION_(\d+)""", "app/build.gradle.kts"))

        val ci = File(root, ".github/workflows/ci.yml").readText()
        put("GO_VERSION", capture(ci, """GO_VERSION:\s*"([^"]+)"""", ".github/workflows/ci.yml"))
        put("JAVA_VERSION", capture(ci, """JAVA_VERSION:\s*"([^"]+)"""", ".github/workflows/ci.yml"))

        val catalog = File(root, "android-dpc/gradle/libs.versions.toml").readText()
        put("agp", capture(catalog, """^agp\s*=\s*"([^"]+)"""", "gradle/libs.versions.toml"))
        put("kotlin", capture(catalog, """^kotlin\s*=\s*"([^"]+)"""", "gradle/libs.versions.toml"))

        val wrapper = File(root, "android-dpc/gradle/wrapper/gradle-wrapper.properties").readText()
        put("gradle", capture(wrapper, """gradle-([0-9][0-9.]*)-bin\.zip""", "gradle-wrapper.properties"))
    }

    private data class Stated(val file: File, val line: Int, val key: String, val value: String)

    /** Every `identifier value` pair a document states, by mechanism 1. */
    private val stated: List<Stated> = docs.flatMap { file ->
        file.readLines().flatMapIndexed { i, line ->
            IDENTIFIERS.findAll(line).map {
                Stated(file, i + 1, it.groupValues[1], it.groupValues[2])
            }
        }
    }

    @Test
    fun `the scan read the build files and the documents`() {
        // First: every assertion below is "this list of disagreements is empty", and an empty list is
        // also what a scan that read nothing produces.
        assertTrue(
            "found only ${docs.size} Markdown documents under $root; the walk is reading the wrong " +
                "directory and every check below is vacuous",
            docs.size >= 8,
        )
        assertEquals(
            "the authority did not parse every key it names; a key that resolves to nothing silently " +
                "stops policing its identifier",
            emptyList<String>(),
            authority.filterValues { it.isEmpty() }.keys.toList(),
        )
        assertTrue(
            "only ${stated.size} identifier-anchored version claims were found across the doc set, " +
                "which is fewer than it has ever had; the pattern has stopped matching and mechanism " +
                "1 is checking nothing",
            stated.size >= 6,
        )

        // The authority must be able to say no. Without this, a `capture` that returned the empty
        // string for every key would agree with nothing and be reported as agreeing with everything.
        assertEquals("29", authority["minSdk"])
        assertEquals(
            "the identifier pattern did not read a plain `minSdk 37` claim",
            listOf("minSdk" to "37"),
            IDENTIFIERS.findAll("the DPC sets `minSdk 37` today")
                .map { it.groupValues[1] to it.groupValues[2] }.toList(),
        )
    }

    @Test
    fun `every version a document states next to its identifier is the one the build uses`() {
        val wrong = stated.filter { authority[it.key] != it.value }
        assertEquals(
            "these documents state a version the build does not use; the build file is the " +
                "authority and the document is what has to move: " +
                wrong.joinToString("; ") {
                    "${it.file.relativeTo(root).path}:${it.line} says ${it.key} ${it.value}, " +
                        "build says ${authority[it.key]}"
                },
            emptyList<Stated>(), wrong,
        )
    }

    @Test
    fun `every registered prose claim still matches, and still states the right version`() {
        val text = docs.associateWith { it.readText() }
        val dead = CLAIMS.filter { (pattern, _) -> text.values.none { pattern.containsMatchIn(it) } }
        assertEquals(
            "these registered claims matched nothing, so they are no longer policing the sentence " +
                "they were written for — the sentence was reworded, and rewording it is exactly when " +
                "its version goes stale: " + dead.joinToString("; ") { it.first.pattern },
            emptyList<Pair<Regex, List<String>>>(), dead,
        )

        val wrong = mutableListOf<String>()
        for ((pattern, keys) in CLAIMS) {
            for ((file, body) in text) {
                for (match in pattern.findAll(body)) {
                    keys.forEachIndexed { group, key ->
                        val found = match.groupValues[group + 1]
                        if (found != authority[key]) {
                            val line = body.take(match.range.first).count { it == '\n' } + 1
                            wrong += "${file.relativeTo(root).path}:$line says $key $found, " +
                                "build says ${authority[key]}"
                        }
                    }
                }
            }
        }
        assertEquals(
            "these sentences state a version the build does not use: " + wrong.joinToString("; "),
            emptyList<String>(), wrong,
        )
    }

    private companion object {
        val SKIPPED_DIRS = setOf("build", ".git", ".gradle", ".idea", ".kotlin", "node_modules", "scratchpad")

        /**
         * Mechanism 1: a build-file identifier, then its value.
         *
         * The separator allows the shapes the documents actually use — `` `minSdk 29` ``,
         * `minSdk = 29`, `minSdk is 29`, `jvmTarget` 21 — and nothing that would let an unrelated
         * number three words later be read as the claim. `minSdk is 29 rather than the 26 this
         * section first named` is a correct sentence and has to stay green.
         */
        val IDENTIFIERS = Regex(
            """\b(compileSdkMinor|compileSdk|buildToolsVersion|minSdk|targetSdk|versionName|jvmTarget|""" +
                """sourceCompatibility|targetCompatibility|GO_VERSION|JAVA_VERSION)\b[`"]?\s*(?:=|is|:)?\s*[`"]?""" +
                """([0-9]+(?:\.[0-9]+)*)""",
        )

        /**
         * Mechanism 2: sentences that state a version without naming an identifier.
         *
         * Each entry is one regex and the authority keys its capture groups correspond to, in order.
         * Narrow on purpose — each is pinned to the phrasing of the one sentence it guards, so it
         * cannot wander onto a floor or a historical number stated nearby.
         */
        val CLAIMS: List<Pair<Regex, List<String>>> = listOf(
            Regex("""Prerequisites: Go ([0-9]+(?:\.[0-9]+)*)\+, JDK (\d+)""") to listOf("GO_VERSION", "JAVA_VERSION"),
            Regex("""platform (\d+\.\d+) and build-tools ([0-9]+(?:\.[0-9]+)*)""") to listOf("compileSdkPlatform", "buildToolsVersion"),
            Regex("""Gradle wrapper pins ([0-9]+(?:\.[0-9]+)*), on AGP ([0-9]+(?:\.[0-9]+)*) and Kotlin ([0-9]+(?:\.[0-9]+)*)""")
                to listOf("gradle", "agp", "kotlin"),
        )

        /** Reads one value out of an authority file, or fails naming the file rather than the regex. */
        fun capture(text: String, pattern: String, file: String): String =
            Regex(pattern, RegexOption.MULTILINE).find(text)?.groupValues?.get(1)
                ?: throw AssertionError(
                    "no match for /$pattern/ in $file; the authority for that version cannot be read, " +
                        "and a guard that cannot read its authority must not report agreement",
                )
    }
}
