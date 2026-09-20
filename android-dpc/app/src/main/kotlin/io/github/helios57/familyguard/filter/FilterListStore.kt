package io.github.helios57.familyguard.filter

import java.io.File
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/** What the device knows about the list it is filtering from. Reported to the parent verbatim. */
data class FilterListState(
    val url: String = "",
    val sha256: String = "",
    val bytes: Long = 0,
    val rules: Int = 0,
    val fetchedAt: Long = 0,
) {
    fun isEmpty(): Boolean = sha256.isEmpty()
}

/** What one attempt to refresh the list did. */
sealed interface RefreshResult {

    /** New bytes, compiled and cached. */
    data class Updated(val state: FilterListState, val report: CompileReport) : RefreshResult

    /** The server sent the same list. The cache is untouched and still good. */
    data class Unchanged(val state: FilterListState) : RefreshResult

    /**
     * Nothing was fetched, and **the cache is untouched**.
     *
     * That last part is the whole contract. A failed refresh must never leave the phone with a
     * half-written list or none at all: the filter would silently stop filtering, and the only
     * visible sign would be advertising coming back weeks later.
     */
    data class Failed(val reason: String) : RefreshResult
}

/**
 * The filter list on disk: fetched, hashed, compiled, and kept.
 *
 * **This ships the fetcher and never the data.** The lists this is pointed at are published under
 * the GPL and this app is MIT; a copy of one in this repository would relicense the repository.
 * More practically, a list baked into an APK is a list that is months old by the time a child
 * installs it. So the URL comes from the parent's policy, the bytes arrive at run time, and nothing
 * in this project asserts anything about their content.
 *
 * ### Why the hash decides, rather than a header
 *
 * `If-Modified-Since` and `ETag` are what a cache normally uses and both are a claim by the server
 * about its own state. The hash is a claim about the bytes that actually arrived, which is the only
 * thing that matters here: a list that was truncated by a captive portal has a perfectly good 200
 * and a perfectly good ETag. Comparing what was received against what is held also means a
 * re-download that changes nothing costs one compile that is skipped, rather than one that is done
 * for no reason on every sync.
 *
 * ### Nothing is replaced until all of it has arrived
 *
 * The download goes to a temporary file and is renamed over the cache only after the whole body has
 * been read and hashed. A phone that loses signal mid-download keeps the list it had.
 */
class FilterListStore(
    private val directory: File,
    private val open: (String) -> InputStream = ::openHttps,
    private val now: () -> Long = System::currentTimeMillis,
    private val log: (String) -> Unit = {},
) {

    private val listFile = File(directory, "list.txt")
    private val metaFile = File(directory, "list.meta")

    /** What is cached, or an empty state if nothing is. */
    fun state(): FilterListState {
        if (!listFile.isFile || !metaFile.isFile) return FilterListState()
        val fields = try {
            metaFile.readLines().mapNotNull { line ->
                val at = line.indexOf('=')
                if (at <= 0) null else line.substring(0, at) to line.substring(at + 1)
            }.toMap()
        } catch (e: Exception) {
            log("the list metadata could not be read: ${e.message}")
            return FilterListState()
        }
        return FilterListState(
            url = fields["url"].orEmpty(),
            sha256 = fields["sha256"].orEmpty(),
            bytes = fields["bytes"]?.toLongOrNull() ?: 0,
            rules = fields["rules"]?.toIntOrNull() ?: 0,
            fetchedAt = fields["fetchedAt"]?.toLongOrNull() ?: 0,
        )
    }

    /**
     * Compile whatever is cached, without touching the network.
     *
     * Called on every start, because the tunnel must be able to come up on a phone that has not
     * reached the internet yet — a child's first minute after a reboot on mobile data is exactly
     * when a filter that waited for a download would be off.
     */
    fun compiled(): Pair<DomainIndex, CompileReport>? {
        if (!listFile.isFile) return null
        return try {
            listFile.bufferedReader().use { reader -> FilterCompiler.compile(reader.lineSequence()) }
        } catch (e: Exception) {
            log("the cached list could not be compiled: ${e.message}")
            null
        }
    }

    fun refresh(url: String): RefreshResult {
        if (url.isBlank()) return RefreshResult.Failed("no list url is configured")
        if (!url.startsWith("https://")) {
            // Plain HTTP would let anything on the path decide what this phone blocks, which
            // includes deciding that it blocks the control plane.
            return RefreshResult.Failed("a filter list must be fetched over https")
        }
        val known = state()
        if (!directory.isDirectory && !directory.mkdirs()) {
            return RefreshResult.Failed("could not create ${directory.name}")
        }

        val temporary = File(directory, "list.download")
        val digest = MessageDigest.getInstance("SHA-256")
        var total = 0L
        try {
            open(url).use { input ->
                temporary.outputStream().buffered().use { output ->
                    val buffer = ByteArray(64 * 1024)
                    while (true) {
                        val read = input.read(buffer)
                        if (read < 0) break
                        total += read
                        if (total > MAX_BYTES) {
                            return failed(temporary, "the list is larger than ${MAX_BYTES / 1024 / 1024} MB")
                        }
                        digest.update(buffer, 0, read)
                        output.write(buffer, 0, read)
                    }
                }
            }
        } catch (e: Exception) {
            return failed(temporary, "could not fetch the list: ${e.message}")
        }
        if (total == 0L) return failed(temporary, "the list was empty")

        val hash = digest.digest().joinToString("") { "%02x".format(it) }
        if (hash == known.sha256 && known.url == url && listFile.isFile) {
            temporary.delete()
            return RefreshResult.Unchanged(known)
        }

        // Compile BEFORE replacing the cache. A list that cannot be compiled must not be able to
        // take away the one that can — and a compile that yields nothing is a fetch that succeeded
        // and a filter that stopped working, which is the failure with no symptom.
        val compiled = try {
            temporary.bufferedReader().use { reader -> FilterCompiler.compile(reader.lineSequence()) }
        } catch (e: Exception) {
            return failed(temporary, "the list could not be compiled: ${e.message}")
        }
        if (compiled.first.ruleCount == 0) {
            return failed(temporary, "the list compiled to no rules at all (${compiled.second})")
        }

        if (!temporary.renameTo(listFile)) {
            return failed(temporary, "could not replace the cached list")
        }
        val fresh = FilterListState(
            url = url,
            sha256 = hash,
            bytes = total,
            rules = compiled.first.ruleCount,
            fetchedAt = now(),
        )
        try {
            metaFile.writeText(
                "url=${fresh.url}\nsha256=${fresh.sha256}\nbytes=${fresh.bytes}\n" +
                    "rules=${fresh.rules}\nfetchedAt=${fresh.fetchedAt}\n",
            )
        } catch (e: Exception) {
            log("the list was replaced but its metadata was not written: ${e.message}")
        }
        return RefreshResult.Updated(fresh, compiled.second)
    }

    /** Forget the cached list. Used when a parent switches the filter off for good. */
    fun clear() {
        listFile.delete()
        metaFile.delete()
    }

    private fun failed(temporary: File, reason: String): RefreshResult {
        temporary.delete()
        log(reason)
        return RefreshResult.Failed(reason)
    }

    companion object {
        /**
         * The biggest list this will accept.
         *
         * The lists worth fetching are a few megabytes of text; this is several times that, so it
         * refuses only a server that is misbehaving. Without a cap a redirect to something large is
         * a phone with a full disk, which breaks far more than advertising.
         */
        const val MAX_BYTES = 32L * 1024 * 1024

        /** Bounded so a hung server cannot leave the sync thread waiting for it indefinitely. */
        const val TIMEOUT_MILLIS = 30_000
    }
}

private fun openHttps(url: String): InputStream {
    val connection = URL(url).openConnection() as HttpURLConnection
    connection.connectTimeout = FilterListStore.TIMEOUT_MILLIS
    connection.readTimeout = FilterListStore.TIMEOUT_MILLIS
    connection.instanceFollowRedirects = true
    connection.setRequestProperty("Accept-Encoding", "gzip")
    val status = connection.responseCode
    if (status != HttpURLConnection.HTTP_OK) {
        connection.disconnect()
        throw IllegalStateException("the list server answered $status")
    }
    val stream = connection.inputStream
    return if (connection.contentEncoding?.equals("gzip", ignoreCase = true) == true) {
        java.util.zip.GZIPInputStream(stream)
    } else {
        stream
    }
}
