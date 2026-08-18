package io.github.helios57.familyguard.net

import java.io.BufferedReader
import java.io.IOException
import java.io.InputStreamReader
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.withContext

/**
 * Holds the server-sent-event stream open and reconnects when it drops.
 *
 * **An event only wakes a sync; the fetch is what delivers.** Nothing here reads the event's data
 * as state. The server's `Event` deliberately carries identifiers and never values, and the reason
 * is recorded on the server side too: a push that delivered a command would let a phone in a tunnel
 * be shown as having received an alarm it never got. So this class's entire output is "something
 * changed, go and look", and the looking is what records that it happened.
 *
 * The `connected` frame is treated as the definition of a working stream, and it is the only thing
 * that resets the backoff — see [Backoff]. Everything else is forwarded to [onWake].
 */
class EventStream(
    private val api: ApiClient,
    private val backoff: Backoff = Backoff(),
    private val io: CoroutineDispatcher = Dispatchers.IO,
    private val onWake: suspend (SseEvent) -> Unit,
) {
    /** Set when the server has refused this device's credential; the caller must re-enroll. */
    var lastFatal: ApiException? = null
        private set

    /**
     * Runs until the coroutine is cancelled, or until the server refuses the credential.
     *
     * A non-retryable refusal ends the loop rather than backing off. A device whose token has been
     * revoked — the parent deleted it from the console — cannot recover by waiting, and a phone
     * that retries a dead credential every few minutes for the rest of its life is a battery
     * complaint with no other symptom.
     */
    suspend fun run() {
        while (true) {
            currentCoroutineContext().ensureActive()
            try {
                readOnce()
            } catch (e: CancellationException) {
                throw e
            } catch (e: ApiException) {
                if (!e.retryable) {
                    lastFatal = e
                    return
                }
            } catch (_: IOException) {
                // A dropped stream is the normal case, not an error: the server closes every
                // connection at fifteen minutes on purpose, and a phone changing networks closes
                // them far more often than that.
            }
            delay(backoff.nextDelayMillis())
        }
    }

    private suspend fun readOnce() {
        val connection = withContext(io) { api.openStream() }
        try {
            val parser = SseParser()
            val reader = withContext(io) {
                BufferedReader(InputStreamReader(connection.inputStream, Charsets.UTF_8))
            }
            while (true) {
                currentCoroutineContext().ensureActive()
                val line = withContext(io) { reader.readLine() } ?: return
                val event = parser.line(line) ?: continue
                if (event.type == CONNECTED) {
                    // Established, not merely accepted. A proxy in front of a restarting backend
                    // accepts the socket and closes it at once; resetting on connect rather than on
                    // this frame would hold such a phone at the base delay indefinitely.
                    backoff.reset()
                    continue
                }
                onWake(event)
            }
        } finally {
            withContext(io) { connection.disconnect() }
        }
    }

    private companion object {
        const val CONNECTED = "connected"
    }
}
