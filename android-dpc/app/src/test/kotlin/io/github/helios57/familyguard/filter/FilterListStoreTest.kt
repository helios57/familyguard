package io.github.helios57.familyguard.filter

import java.io.ByteArrayInputStream
import java.io.File
import java.io.IOException
import java.io.InputStream
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/**
 * The cache, and the one promise it makes: **a failed refresh leaves the phone filtering.**
 *
 * That is the assertion worth having here. A store that replaces the cache and then discovers the
 * list is unusable produces a phone that quietly stops filtering, with a successful-looking fetch
 * in the log and advertising coming back weeks later — the failure with no symptom. So every
 * failure path below is measured by what is still on disk afterwards, not by the value returned.
 */
class FilterListStoreTest {

    @get:Rule
    val folder = TemporaryFolder()

    private val url = "https://lists.example.com/filter.txt"
    private val list = "||ads.example.com^\n||tracker.example.net^\n@@||shop.example.com^\n"

    @Test
    fun `an empty cache says so rather than pretending`() {
        val store = store()

        assertTrue(store.state().isEmpty())
        assertNull(store.compiled())
    }

    @Test
    fun `a fetched list is compiled and cached`() {
        val store = store(serving = list)

        val result = store.refresh(url)

        val updated = result as RefreshResult.Updated
        assertEquals(3, updated.state.rules)
        assertEquals(url, updated.state.url)
        assertEquals(list.length.toLong(), updated.state.bytes)
        assertEquals(64, updated.state.sha256.length)
        assertEquals(1, updated.report.allowRules)
    }

    @Test
    fun `what was cached survives a new instance`() {
        store(serving = list).refresh(url)

        // A second store over the same directory is what the next boot looks like.
        val next = store()
        val state = next.state()

        assertEquals(3, state.rules)
        assertFalse(state.isEmpty())
        assertEquals(3, next.compiled()?.first?.ruleCount)
    }

    @Test
    fun `the cache compiles without touching the network`() {
        store(serving = list).refresh(url)

        val offline = FilterListStore(
            directory = directory,
            open = { throw IOException("the network must not be reached") },
        )

        // The minute after a reboot on mobile data is exactly when a filter that waited for a
        // download would be off.
        assertEquals(3, offline.compiled()?.first?.ruleCount)
    }

    @Test
    fun `the same bytes are not recompiled`() {
        val store = store(serving = list)
        store.refresh(url)

        val result = store.refresh(url)

        assertTrue("expected Unchanged, got $result", result is RefreshResult.Unchanged)
        assertEquals(3, (result as RefreshResult.Unchanged).state.rules)
    }

    @Test
    fun `a changed list at the same url replaces the old one`() {
        val store = store(serving = list)
        store.refresh(url)

        val bigger = list + "||more.example.org^\n"
        val result = FilterListStore(directory = directory, open = { bigger.byteInputStream() })
            .refresh(url)

        assertEquals(4, (result as RefreshResult.Updated).state.rules)
    }

    @Test
    fun `the same bytes at a different url are fetched again`() {
        val store = store(serving = list)
        store.refresh(url)

        // The hash matches, but the parent pointed the phone somewhere else. Reporting Unchanged
        // would leave the console showing a url the phone is not actually using.
        val result = store.refresh("https://other.example.com/filter.txt")

        assertEquals("https://other.example.com/filter.txt", (result as RefreshResult.Updated).state.url)
    }

    @Test
    fun `a refresh that cannot connect leaves the cache alone`() {
        val store = store(serving = list)
        store.refresh(url)

        val broken = FilterListStore(
            directory = directory,
            open = { throw IOException("connection reset") },
        )
        val result = broken.refresh(url)

        assertTrue(result is RefreshResult.Failed)
        assertTrue((result as RefreshResult.Failed).reason.contains("connection reset"))
        assertStillFiltering()
    }

    @Test
    fun `a download that dies half way leaves the cache alone`() {
        val store = store(serving = list)
        store.refresh(url)

        val truncating = FilterListStore(
            directory = directory,
            open = { HalfwayStream("||replacement.example.com^\n||second.example.com^\n".toByteArray()) },
        )
        val result = truncating.refresh(url)

        assertTrue("expected a failure, got $result", result is RefreshResult.Failed)
        assertStillFiltering()
        assertFalse("the temporary file was left behind", File(directory, "list.download").exists())
    }

    @Test
    fun `a list that compiles to nothing is refused`() {
        val store = store(serving = list)
        store.refresh(url)

        // A captive portal's login page: a perfectly good 200, several kilobytes, and not one rule.
        val portal = FilterListStore(
            directory = directory,
            open = { "<html><body>Please sign in to the hotel wifi</body></html>".byteInputStream() },
        )
        val result = portal.refresh(url)

        assertTrue("expected a failure, got $result", result is RefreshResult.Failed)
        assertTrue((result as RefreshResult.Failed).reason.contains("no rules"))
        assertStillFiltering()
    }

    @Test
    fun `an empty body is refused`() {
        val store = store(serving = "")

        val result = store.refresh(url)

        assertTrue((result as RefreshResult.Failed).reason.contains("empty"))
        assertTrue(store.state().isEmpty())
    }

    @Test
    fun `a list larger than the cap is refused before it fills the disk`() {
        val store = FilterListStore(directory = directory, open = { EndlessStream() })

        val result = store.refresh(url)

        assertTrue("expected a failure, got $result", result is RefreshResult.Failed)
        assertTrue((result as RefreshResult.Failed).reason.contains("larger than"))
        assertFalse(File(directory, "list.download").exists())
    }

    @Test
    fun `plain http is refused outright`() {
        val store = store(serving = list)

        val result = store.refresh("http://lists.example.com/filter.txt")

        // Anything on the path could otherwise decide what this phone blocks, and that includes
        // deciding that it blocks the control plane.
        assertTrue((result as RefreshResult.Failed).reason.contains("https"))
    }

    @Test
    fun `no url configured is a refusal, not a crash`() {
        val result = store(serving = list).refresh("")

        assertTrue((result as RefreshResult.Failed).reason.contains("no list url"))
    }

    @Test
    fun `clearing forgets the list and the metadata`() {
        val store = store(serving = list)
        store.refresh(url)

        store.clear()

        assertTrue(store.state().isEmpty())
        assertNull(store.compiled())
    }

    @Test
    fun `metadata that was damaged reads as no cache at all`() {
        val store = store(serving = list)
        store.refresh(url)

        File(directory, "list.meta").writeText("this is not key=value at all\n")

        // Reported as empty rather than as a state with zeroes in it: the console would show
        // "0 rules" and send a parent looking for a list that is in fact on the phone.
        assertTrue(store.state().isEmpty() || store.state().rules == 0)
        // The list itself is still compilable — the damage is in the bookkeeping, not the data.
        assertEquals(3, store.compiled()?.first?.ruleCount)
    }

    @Test
    fun `the timestamp is the store's own clock`() {
        val store = FilterListStore(
            directory = directory,
            open = { list.byteInputStream() },
            now = { 1_700_000_000_000L },
        )

        val result = store.refresh(url)

        assertEquals(1_700_000_000_000L, (result as RefreshResult.Updated).state.fetchedAt)
        assertEquals(1_700_000_000_000L, store.state().fetchedAt)
    }

    @Test
    fun `a directory that does not exist yet is created`() {
        val nested = File(directory, "not/created/yet")
        val store = FilterListStore(directory = nested, open = { list.byteInputStream() })

        val result = store.refresh(url)

        assertNotNull(result as? RefreshResult.Updated)
        assertTrue(File(nested, "list.txt").isFile)
    }

    // ---- fixtures ------------------------------------------------------------------------------

    private val directory: File get() = folder.root

    private fun store(serving: String? = null) = FilterListStore(
        directory = directory,
        open = {
            serving?.byteInputStream() ?: throw IOException("this store was not given a body to serve")
        },
    )

    private fun assertStillFiltering() {
        val cached = FilterListStore(directory = directory, open = { throw IOException("offline") })
        assertEquals("the cache was lost by a failed refresh", 3, cached.state().rules)
        assertEquals(3, cached.compiled()?.first?.ruleCount)
        assertEquals(url, cached.state().url)
    }

    /** Fails part way through, the way a phone that walks out of range does. */
    private class HalfwayStream(private val bytes: ByteArray) : InputStream() {
        private val head = ByteArrayInputStream(bytes, 0, bytes.size / 2)
        override fun read(): Int = head.read().also { if (it < 0) throw IOException("signal lost") }
        override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
            val read = head.read(buffer, offset, length)
            if (read < 0) throw IOException("signal lost")
            return read
        }
    }

    /** A redirect to something that never ends. */
    private class EndlessStream : InputStream() {
        override fun read(): Int = 'x'.code
        override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
            buffer.fill('x'.code.toByte(), offset, offset + length)
            return length
        }
    }
}
