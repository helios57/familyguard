package io.github.helios57.familyguard.status

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The status block speaks the phone's language, and its three sources of words cannot drift:
 * [StatusWords.ENGLISH] (what the JVM tests read), `values/strings.xml` (what an English phone
 * reads) and `values-de/strings.xml` (what the family's phones read). Until 0.6.34 the block was
 * English sentences hardcoded in the composer, on German phones.
 */
class StatusWordsTest {

    private fun words(dir: String): Map<String, String> {
        val file = listOf("src/main/res/$dir/strings.xml", "app/src/main/res/$dir/strings.xml")
            .map(::File).first { it.exists() }
        val doc = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file)
        val nodes = doc.getElementsByTagName("string")
        return (0 until nodes.length).map { nodes.item(it) }
            .map { it.attributes.getNamedItem("name").nodeValue to it.textContent }
            .filter { it.first.startsWith("status_w_") }
            .associate { it.first.removePrefix("status_w_") to it.second.replace("\\'", "'").replace("\\\"", "\"") }
    }

    @Test
    fun `the English words in code are the English resources, word for word`() {
        val english = words("values")
        assertTrue("no status_w_ strings were read, so this compares nothing", english.size > 20)
        assertEquals(english.keys, StatusWords.ENGLISH.keys)
        for (key in english.keys) assertEquals("status_w_$key", english[key], StatusWords.ENGLISH[key])
    }

    @Test
    fun `the German resources carry every word, with the same placeholders`() {
        val german = words("values-de")
        assertEquals(StatusWords.ENGLISH.keys, german.keys)
        val placeholder = Regex("%\\d\\$[sd]")
        for ((key, text) in german) {
            assertEquals(
                "status_w_$key takes different arguments in German",
                placeholder.findAll(StatusWords.ENGLISH[key]).map { it.value }.toSet(),
                placeholder.findAll(text).map { it.value }.toSet(),
            )
        }
    }

    @Test
    fun `the phone reads every word from a resource`() {
        assertEquals(StatusWords.ENGLISH.keys, STATUS_WORD_IDS.keys)
    }

    @Test
    fun `a German phone's status block is German`() {
        val german = StatusWords(words("values-de"))
        val status = deviceStatus(
            StatusFacts(
                deviceId = "dev-1", serverHost = "guard.example.com", deviceOwner = true,
                powerExempt = false, exactAlarms = true, releasedSinceMillis = null,
                appliedPolicyVersion = 3, cachedPolicyVersion = 3, lastServerContactMillis = 0,
                screenTimeTodayMillis = null, screenTimeUnavailableReason = "usage access off",
                quotaMinutes = 90, unreportedRecoveryAttempts = 2, nowMillis = 1_000_000,
                usageAccess = false,
            ),
            german,
        )
        val rendered = status.lines.joinToString("\n") { "${it.label}: ${it.value}" }
        for (english in listOf("Enrollment", "set up as", "restricted", "minutes", "never", "recovery attempt")) {
            assertTrue("\"$english\" on a German screen:\n$rendered", english !in rendered)
        }
        assertTrue(rendered, "Bildschirmzeit heute: nicht gemessen: der Nutzungsdatenzugriff ist aus" in rendered)
        assertEquals(StatusAction.ALLOW_USAGE_ACCESS, status.line(StatusLabels.SCREEN_TIME).action)
        assertEquals("vor 2 Stunden", ago(2 * 3_600_000L, german))
    }

    /** No usage access: plain words and the switch. Any other cause: the reader's reason, no button. */
    @Test
    fun `screen time offers the usage-access switch exactly when that is what is missing`() {
        fun line(access: Boolean?) = deviceStatus(
            StatusFacts(
                deviceId = "d", serverHost = null, deviceOwner = true, powerExempt = true, exactAlarms = true,
                releasedSinceMillis = null, appliedPolicyVersion = 1, cachedPolicyVersion = 1,
                lastServerContactMillis = 999_000, screenTimeTodayMillis = null,
                screenTimeUnavailableReason = "the platform returned nothing", quotaMinutes = 0,
                unreportedRecoveryAttempts = 0, nowMillis = 1_000_000, usageAccess = access,
            ),
        ).line(StatusLabels.SCREEN_TIME)
        assertEquals(StatusAction.ALLOW_USAGE_ACCESS, line(false).action)
        assertEquals(null, line(true).action)
        assertEquals("the platform returned nothing", line(true).value)
        assertEquals(null, line(null).action)
    }
}
