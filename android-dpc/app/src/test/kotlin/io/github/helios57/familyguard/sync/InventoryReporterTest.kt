package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.device.InstalledApp
import io.github.helios57.familyguard.device.InstalledAppReader
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

/**
 * When the inventory is sent, when it is not, and what a failure must not look like.
 *
 * The ordering between sending and recording the digest is the whole of the correctness here.
 * Recorded first, a failed send becomes indistinguishable from an unchanged list, and the app a
 * parent is waiting to approve is never reported again — the console shows a healthy phone and no
 * such app (FR-5.1).
 */
class InventoryReporterTest {

    private val reader = FakeReader()
    private val sent = mutableListOf<List<InstalledApp>>()
    private var digest: String = ""
    private var failWith: IOException? = null
    private var storedCount: Int = -1

    private val reporter = InventoryReporter(
        reader = reader,
        send = { apps ->
            failWith?.let { throw it }
            sent += apps
            if (storedCount >= 0) storedCount else apps.size
        },
        lastDigest = { digest },
        recordDigest = { digest = it },
    )

    @Test
    fun `a first inventory is sent`() {
        reader.apps = listOf(app(GAME), app(CHAT))

        val result = reporter.report()

        assertEquals(InventoryResult.Sent(sent = 2, stored = 2), result)
        assertEquals(1, sent.size)
    }

    /** A phone syncs on every server event; re-sending two hundred apps each time is uplink burnt. */
    @Test
    fun `an unchanged inventory is not sent again`() {
        reader.apps = listOf(app(GAME), app(CHAT))
        reporter.report()

        val result = reporter.report()

        assertEquals(InventoryResult.Unchanged, result)
        assertEquals(1, sent.size)
    }

    @Test
    fun `an installed app makes the inventory change`() {
        reader.apps = listOf(app(GAME))
        reporter.report()
        reader.apps = listOf(app(GAME), app(CHAT))

        assertTrue(reporter.report() is InventoryResult.Sent)
        assertEquals(2, sent.size)
    }

    @Test
    fun `an uninstalled app makes the inventory change`() {
        reader.apps = listOf(app(GAME), app(CHAT))
        reporter.report()
        reader.apps = listOf(app(GAME))

        assertTrue(reporter.report() is InventoryResult.Sent)
    }

    /**
     * The digest covers every field that is sent. An app renamed by an update, or one that becomes a
     * system app in an OTA, is a change the console should show — hashing package names alone would
     * make those permanently invisible.
     */
    @Test
    fun `a renamed app makes the inventory change`() {
        reader.apps = listOf(app(GAME, label = "Game"))
        reporter.report()
        reader.apps = listOf(app(GAME, label = "Game Deluxe"))

        assertTrue(reporter.report() is InventoryResult.Sent)
    }

    @Test
    fun `an app that becomes a system app makes the inventory change`() {
        reader.apps = listOf(app(GAME, systemApp = false))
        reporter.report()
        reader.apps = listOf(app(GAME, systemApp = true))

        assertTrue(reporter.report() is InventoryResult.Sent)
    }

    /** Order is not a change: `PackageManager` does not promise one, and the reader sorts anyway. */
    @Test
    fun `the same apps in a different order are unchanged`() {
        reader.apps = listOf(app(GAME), app(CHAT))
        reporter.report()
        reader.apps = listOf(app(CHAT), app(GAME))

        assertEquals(InventoryResult.Unchanged, reporter.report())
    }

    /**
     * A label is arbitrary user-visible text, so it can contain whatever character looked safe to
     * separate on. These two inventories are byte-identical under a `:`-joined encoding and differ
     * only in where the boundary falls — a collision that does not look like a bug, it looks like an
     * inventory that stopped changing.
     */
    @Test
    fun `two inventories that a delimited encoding would confuse have different digests`() {
        reader.apps = listOf(app("a", label = "b:c"))
        reporter.report()
        val first = digest

        reader.apps = listOf(app("a:b", label = "c"))
        reporter.report()

        assertNotEquals(first, digest)
        assertEquals("both were sent, so both were seen as changes", 2, sent.size)
    }

    // ---- failures --------------------------------------------------------------------------------

    /** The ordering that matters: the digest must not advance past a send that did not happen. */
    @Test
    fun `a failed send is retried on the next report`() {
        reader.apps = listOf(app(GAME))
        failWith = IOException("no route to host")

        val failure = reporter.report()

        assertTrue(failure is InventoryResult.Failed)
        assertEquals("the digest advanced past a send that failed", "", digest)

        failWith = null
        assertTrue(reporter.report() is InventoryResult.Sent)
        assertEquals(1, sent.size)
    }

    /**
     * An inventory that cannot be read is never sent as an empty one. Empty does not read as a broken
     * device — it reads as a child with a bare phone, and every app rule a parent writes afterwards is
     * against a list the console does not have.
     */
    @Test
    fun `an unreadable inventory is not measured, and nothing is sent`() {
        reader.apps = null
        reader.reason = "package visibility is filtering the list"

        val result = reporter.report()

        assertEquals(InventoryResult.NotMeasured("package visibility is filtering the list"), result)
        assertTrue(sent.isEmpty())
        assertEquals("", digest)
    }

    /**
     * A device really can have no apps the reader can see only in a test, but the distinction is what
     * the whole [InventoryResult.NotMeasured] branch rests on: an *empty* list is a measurement, and
     * it is sent.
     */
    @Test
    fun `an empty inventory is a measurement and is sent`() {
        reader.apps = emptyList()

        assertEquals(InventoryResult.Sent(sent = 0, stored = 0), reporter.report())
    }

    /** [InventoryResult.Sent.stored] is the server's own count, which may differ from what was sent. */
    @Test
    fun `what the server says it stored is reported, not what was sent`() {
        reader.apps = listOf(app(GAME), app(CHAT))
        storedCount = 1

        assertEquals(InventoryResult.Sent(sent = 2, stored = 1), reporter.report())
    }

    private fun app(pkg: String, label: String = pkg, systemApp: Boolean = false) =
        InstalledApp(packageName = pkg, label = label, systemApp = systemApp)

    private class FakeReader : InstalledAppReader {
        var apps: List<InstalledApp>? = emptyList()
        var reason: String = "not asked yet"
        override fun installed(): List<InstalledApp>? = apps
        override fun unavailableReason(): String = reason
    }

    private companion object {
        const val GAME = "com.example.game"
        const val CHAT = "com.example.chat"
    }
}
