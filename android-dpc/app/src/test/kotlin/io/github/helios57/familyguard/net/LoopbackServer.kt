package io.github.helios57.familyguard.net

import java.io.BufferedOutputStream
import java.io.IOException
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.CopyOnWriteArrayList
import kotlin.concurrent.thread

/** One request as it arrived on the wire, headers lower-cased for lookup. */
data class RecordedRequest(
    val method: String,
    val path: String,
    val headers: Map<String, String>,
    val body: String,
) {
    fun header(name: String): String? = headers[name.lowercase()]
}

/**
 * One response, written verbatim.
 *
 * [stream] omits `Content-Length`, so the body ends when the connection closes — which is how a
 * server-sent-event stream ends, and the case the reconnect logic exists for.
 */
class HttpResponse(
    val status: Int,
    val reason: String = "OK",
    val contentType: String = "application/json",
    val body: String = "",
    val stream: Boolean = false,
)

/**
 * A minimal HTTP/1.1 server on loopback, for tests that need real sockets.
 *
 * Written by hand rather than taken from `com.sun.net.httpserver`, which is not on the Android unit
 * test compile classpath, and rather than a mock, which would test this code's opinion of HTTP
 * instead of HTTP. Two of the behaviours under test only exist on a real connection:
 * `HttpURLConnection.getInputStream()` throws on a 4xx so the server's own explanation has to come
 * from `getErrorStream()`, and a stream ends by the peer closing the socket rather than by anyone
 * saying so.
 *
 * Every connection is answered and then closed. That is deliberate: it makes "the stream dropped"
 * the default rather than a special case to arrange. The one exception is [stopAnswering], which is
 * how a test spells "there is no server" without releasing the port.
 */
class LoopbackServer(private var respond: (RecordedRequest) -> HttpResponse) : AutoCloseable {

    private val socket = ServerSocket(0, 0, InetAddress.getLoopbackAddress())

    /** Written from the test thread, read from every connection thread. */
    @Volatile
    private var answering = true

    /** Requests in arrival order. Written from the accept threads, read from the test thread. */
    val requests = CopyOnWriteArrayList<RecordedRequest>()

    val baseUrl: String = "http://${socket.inetAddress.hostAddress}:${socket.localPort}"

    val last: RecordedRequest? get() = requests.lastOrNull()

    init {
        thread(isDaemon = true, name = "loopback-http-accept") {
            while (!socket.isClosed) {
                val connection = try {
                    socket.accept()
                } catch (_: IOException) {
                    return@thread
                }
                thread(isDaemon = true, name = "loopback-http-connection") { serve(connection) }
            }
        }
    }

    /** Replaces the responder. Used by tests that need a different answer per case. */
    fun answerWith(responder: (RecordedRequest) -> HttpResponse) {
        respond = responder
    }

    /**
     * Answer nothing from now on, without giving up the port.
     *
     * This is how a test says *the server cannot be reached*. The obvious spelling — [close], and
     * let the next connection be refused — states that as a property of a port the fixture has just
     * released, and a test can only assert what it owns: once the port is free, anything in the JVM
     * may bind it, and whatever the client then talks to is not this server. `SynchronizerTest`
     * failed exactly that way twice, once on CI, with a recovered device pulled back under
     * management by a policy no server in the test had served. Holding the port makes that
     * impossible rather than unlikely.
     *
     * The request is read and recorded before the connection is dropped, so the client's write
     * always completes and a test can still see that the device tried. What the client gets is
     * `SocketException: Unexpected end of file from server` — an `IOException`, the same branch a
     * refused connection lands in, and measured rather than assumed. `LoopbackServerTest` pins all
     * three halves of that: still bound, request seen, IOException out.
     */
    fun stopAnswering() {
        answering = false
    }

    /**
     * Whether [close] has run.
     *
     * This is the property teardown depends on and the one that separates [close] from
     * [stopAnswering]: the listening socket is shut, so the port goes back to the OS and the accept
     * thread ends. It is exposed because it is the only *ownable* way to assert that — once the port
     * is released, nothing observable on the wire is a fact about this server any more.
     */
    val isClosed: Boolean get() = socket.isClosed

    /** Idempotent, and relied upon to be: `@After` closes a server a test may already have closed. */
    override fun close() {
        socket.close()
    }

    private fun serve(connection: Socket) {
        try {
            connection.use {
                val request = read(it.getInputStream()) ?: return
                requests.add(request)
                // `use` closes the connection on the way out, which is the whole response.
                if (!answering) return
                write(it, respond(request))
            }
        } catch (_: IOException) {
            // The client hung up: a reconnecting device does this on every cancelled read, and it is
            // not a fact about the server.
        }
    }

    private fun read(input: InputStream): RecordedRequest? {
        val requestLine = readLine(input) ?: return null
        val parts = requestLine.split(' ')
        if (parts.size < 2) return null

        val headers = mutableMapOf<String, String>()
        while (true) {
            val line = readLine(input) ?: break
            if (line.isEmpty()) break
            val colon = line.indexOf(':')
            if (colon > 0) {
                headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
            }
        }

        val length = headers["content-length"]?.toIntOrNull() ?: 0
        val body = ByteArray(length)
        var read = 0
        while (read < length) {
            val n = input.read(body, read, length - read)
            if (n < 0) break
            read += n
        }
        return RecordedRequest(parts[0], parts[1], headers, String(body, 0, read, Charsets.UTF_8))
    }

    /** Byte-at-a-time on purpose: buffering here would swallow the body of the next request. */
    private fun readLine(input: InputStream): String? {
        val line = StringBuilder()
        while (true) {
            val b = input.read()
            if (b < 0) return if (line.isEmpty()) null else line.toString().removeSuffix("\r")
            if (b == '\n'.code) return line.toString().removeSuffix("\r")
            line.append(b.toChar())
        }
    }

    private fun write(connection: Socket, response: HttpResponse) {
        val body = response.body.toByteArray(Charsets.UTF_8)
        val head = buildString {
            append("HTTP/1.1 ${response.status} ${response.reason}\r\n")
            append("Content-Type: ${response.contentType}\r\n")
            if (!response.stream) append("Content-Length: ${body.size}\r\n")
            append("Connection: close\r\n\r\n")
        }
        val out = BufferedOutputStream(connection.getOutputStream())
        out.write(head.toByteArray(Charsets.US_ASCII))
        out.write(body)
        out.flush()
        // Half-close rather than a bare close: an abrupt reset while the client still has unread
        // bytes buffered turns a legitimate response into an IOException on the client side, and the
        // tests would then be measuring the teardown instead of the response.
        connection.shutdownOutput()
    }
}
