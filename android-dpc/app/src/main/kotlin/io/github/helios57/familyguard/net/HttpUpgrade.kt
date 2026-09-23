package io.github.helios57.familyguard.net

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

/**
 * One HTTP/1.1 request that asks to become a raw byte stream, and the reading of its answer
 * (FR-19.2).
 *
 * `HttpURLConnection` cannot do this — it has no way to hand back the socket after a 101 — and a
 * client library would be the first dependency this device-owner app takes for a single call. The
 * whole exchange is a request line, four headers and a status line, so it is written out.
 *
 * **The answer is read one byte at a time, and that is the point of this class.** Whatever follows
 * the blank line belongs to the stream, not to the HTTP answer, and adbd speaks as soon as it is
 * connected: a buffered reader would swallow the first bytes of the session into a buffer nobody
 * reads again, and adb would hang on a handshake whose first message was eaten. Headers are a few
 * hundred bytes, so reading them unbuffered costs nothing measurable.
 */
object HttpUpgrade {

    /** The protocol token the server's debug relay answers to. */
    const val DEBUG_PROTOCOL = "familyguard-debug"

    /** A response head larger than this is not the server this app was enrolled with. */
    const val MAX_HEAD_BYTES = 16 * 1024

    /** The body of a refusal is read for its message, and no further. */
    const val MAX_REFUSAL_BYTES = 64 * 1024

    data class Head(val status: Int, val headers: Map<String, String>)

    fun request(hostHeader: String, path: String, bearer: String, protocol: String = DEBUG_PROTOCOL): ByteArray =
        (
            "GET $path HTTP/1.1\r\n" +
                "Host: $hostHeader\r\n" +
                "Authorization: Bearer $bearer\r\n" +
                "Connection: Upgrade\r\n" +
                "Upgrade: $protocol\r\n" +
                "Accept: application/json\r\n" +
                "User-Agent: FamilyGuard-DPC\r\n\r\n"
            ).toByteArray(Charsets.ISO_8859_1)

    fun send(output: OutputStream, request: ByteArray) {
        output.write(request)
        output.flush()
    }

    /**
     * The status and headers, consuming exactly the bytes up to and including the blank line.
     * Header names are lower-cased; a repeated header keeps its last value, which is all this needs.
     */
    fun readHead(input: InputStream): Head {
        val bytes = java.io.ByteArrayOutputStream()
        var matched = 0 // how much of "\r\n\r\n" has just been seen
        while (matched < 4) {
            val b = input.read()
            if (b < 0) throw IOException("the server closed the connection before answering")
            bytes.write(b)
            if (bytes.size() > MAX_HEAD_BYTES) throw IOException("the server's answer has no end to its headers")
            matched = when {
                b == '\r'.code && (matched == 0 || matched == 2) -> matched + 1
                b == '\n'.code && (matched == 1 || matched == 3) -> matched + 1
                b == '\r'.code -> 1
                else -> 0
            }
        }
        val lines = bytes.toString(Charsets.ISO_8859_1.name()).split("\r\n")
        val statusLine = lines.first()
        val parts = statusLine.split(' ', limit = 3)
        val status = parts.getOrNull(1)?.toIntOrNull()
        if (!statusLine.startsWith("HTTP/1.") || status == null) {
            throw IOException("the server answered with something that is not HTTP/1.x: ${statusLine.take(80)}")
        }
        val headers = LinkedHashMap<String, String>()
        for (line in lines.drop(1)) {
            val colon = line.indexOf(':')
            if (colon <= 0) continue
            headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
        }
        return Head(status, headers)
    }

    /**
     * The server's own sentence from a refusal, read from the body that follows [head]. The body
     * is JSON in the server's envelope; anything else is reported by its status alone.
     */
    fun refusal(head: Head, input: InputStream): String {
        val length = head.headers["content-length"]?.toIntOrNull()?.coerceIn(0, MAX_REFUSAL_BYTES)
        val body = try {
            if (length != null) {
                val buffer = ByteArray(length)
                var read = 0
                while (read < length) {
                    val n = input.read(buffer, read, length - read)
                    if (n < 0) break
                    read += n
                }
                String(buffer, 0, read, Charsets.UTF_8)
            } else {
                ""
            }
        } catch (e: IOException) {
            ""
        }
        val message = Regex("\"message\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"").find(body)?.groupValues?.get(1)
        return if (message.isNullOrBlank()) "the server answered HTTP ${head.status}"
        else "the server answered HTTP ${head.status}: ${message.replace("\\\"", "\"")}"
    }
}
