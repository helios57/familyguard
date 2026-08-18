package io.github.helios57.familyguard.commands

import io.github.helios57.familyguard.net.DeviceCommand
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** One acknowledgement as the transport received it. */
private data class Ack(
    val id: String,
    val ok: Boolean,
    val result: Map<String, String>,
    val error: String,
)

private class RecordingAck(private val failFor: Set<String> = emptySet()) {
    val sent = mutableListOf<Ack>()

    fun ack(id: String, ok: Boolean, result: Map<String, String>, error: String) {
        if (id in failFor) throw IllegalStateException("the server is unreachable")
        sent += Ack(id, ok, result, error)
    }
}

private fun command(type: String, id: String = "11111111-1111-4111-8111-111111111111") =
    DeviceCommand(id = id, type = type)

private fun id(n: Int) = "1111111%d-1111-4111-8111-111111111111".format(n)

class CommandExecutorTest {

    @Test
    fun `runs the commands in the order the server gave them`() {
        val order = mutableListOf<String>()
        val acks = RecordingAck()
        val executor = CommandExecutor(
            handlers = mapOf(
                "TRIGGER_ALARM" to CommandHandler { order += "start"; CommandOutcome.Done() },
                "STOP_ALARM" to CommandHandler { order += "stop"; CommandOutcome.Done() },
            ),
            ack = acks::ack,
        )

        val report = executor.execute(
            listOf(command("TRIGGER_ALARM", id(1)), command("STOP_ALARM", id(2))),
        )

        assertTrue(report.toString(), report.ok)
        // Reversed, this ends with a siren nobody asked for and no command left to stop it.
        assertEquals(listOf("start", "stop"), order)
        assertEquals(listOf(id(1), id(2)), report.done)
    }

    @Test
    fun `acknowledges each command as it finishes, not in a batch at the end`() {
        val acks = RecordingAck()
        val seenWhenSecondRan = mutableListOf<Int>()
        val executor = CommandExecutor(
            handlers = mapOf(
                "SYNC_POLICY" to CommandHandler {
                    // How many acknowledgements had already been delivered when this handler ran.
                    seenWhenSecondRan += acks.sent.size
                    CommandOutcome.Done()
                },
            ),
            ack = acks::ack,
        )

        executor.execute(listOf(command("SYNC_POLICY", id(1)), command("SYNC_POLICY", id(2))))

        // The second handler must have seen the first acknowledgement already gone. Batching at the
        // end makes a two-second command invisible for as long as the slowest one in the batch, and
        // invisible forever if the process is killed in between.
        assertEquals(listOf(0, 1), seenWhenSecondRan)
    }

    @Test
    fun `an unknown type is acknowledged as failed, naming the type`() {
        val acks = RecordingAck()
        val executor = CommandExecutor(handlers = emptyMap(), ack = acks::ack)

        val report = executor.execute(listOf(command("WIPE_DEVICE", id(1))))

        assertFalse(report.toString(), report.ok)
        // Skipping it silently is indistinguishable at the console from a phone that never woke up,
        // and it is the shape that hides a server deployed ahead of the fleet.
        assertEquals(1, acks.sent.size)
        assertFalse(acks.sent.single().ok)
        assertTrue(acks.sent.single().error, acks.sent.single().error.contains("WIPE_DEVICE"))
    }

    @Test
    fun `a handler that throws fails only its own command`() {
        val acks = RecordingAck()
        val executor = CommandExecutor(
            handlers = mapOf(
                "LOCATE_NOW" to CommandHandler { throw SecurityException("permission revoked") },
                "SYNC_POLICY" to CommandHandler { CommandOutcome.Done() },
            ),
            ack = acks::ack,
        )

        val report = executor.execute(
            listOf(command("LOCATE_NOW", id(1)), command("SYNC_POLICY", id(2))),
        )

        assertEquals(mapOf(id(1) to "permission revoked"), report.failed)
        assertEquals(listOf(id(2)), report.done)
        // Reported as the exception it was, never as "done".
        assertFalse(acks.sent.first().ok)
    }

    @Test
    fun `a command whose acknowledgement cannot be delivered does not stop the batch`() {
        val acks = RecordingAck(failFor = setOf(id(1)))
        val stopped = mutableListOf<String>()
        val executor = CommandExecutor(
            handlers = mapOf(
                "TRIGGER_ALARM" to CommandHandler { CommandOutcome.Done() },
                "STOP_ALARM" to CommandHandler { stopped += "stop"; CommandOutcome.Done() },
            ),
            ack = acks::ack,
        )

        val report = executor.execute(
            listOf(command("TRIGGER_ALARM", id(1)), command("STOP_ALARM", id(2))),
        )

        // A failed `TRIGGER_ALARM` acknowledgement is not a reason to leave the siren on.
        assertEquals(listOf("stop"), stopped)
        assertEquals(setOf(id(1)), report.unacknowledged.keys)
        assertEquals(listOf(id(2)), report.done)
        // Separate from `failed`, because the two are opposite facts: this one probably happened.
        assertTrue(report.failed.isEmpty())
    }

    @Test
    fun `a command whose id is not a UUID is refused rather than executed`() {
        val acks = RecordingAck()
        val ran = mutableListOf<String>()
        val executor = CommandExecutor(
            handlers = mapOf("TRIGGER_ALARM" to CommandHandler { ran += "rang"; CommandOutcome.Done() }),
            ack = acks::ack,
        )

        val report = executor.execute(listOf(command("TRIGGER_ALARM", "../../device/wipe")))

        // Not executed at all: the acknowledgement is addressed by this id, so a siren started here
        // could never be reported and — the one that matters — never be stopped.
        assertTrue(ran.isEmpty())
        assertTrue(acks.sent.isEmpty())
        assertEquals(setOf("../../device/wipe"), report.unacknowledged.keys)
        assertFalse(report.ok)
    }

    @Test
    fun `an empty queue is a clean report`() {
        val acks = RecordingAck()

        val report = CommandExecutor(handlers = emptyMap(), ack = acks::ack).execute(emptyList())

        assertTrue(report.ok)
        assertTrue(report.done.isEmpty())
        assertTrue(acks.sent.isEmpty())
    }

    @Test
    fun `the result a handler produced reaches the acknowledgement`() {
        val acks = RecordingAck()
        val executor = CommandExecutor(
            handlers = mapOf(
                "LOCATE_NOW" to CommandHandler {
                    CommandOutcome.Done(mapOf("source" to "gnss", "age_seconds" to "0"))
                },
            ),
            ack = acks::ack,
        )

        executor.execute(listOf(command("LOCATE_NOW", id(1))))

        assertEquals(mapOf("source" to "gnss", "age_seconds" to "0"), acks.sent.single().result)
        assertEquals("", acks.sent.single().error)
    }
}

class CommandQueueTest {

    @Test
    fun `drains what the fetch returned`() {
        val acks = RecordingAck()
        val queue = CommandQueue(
            fetch = { listOf(command("SYNC_POLICY", id(1))) },
            executor = CommandExecutor(
                handlers = mapOf("SYNC_POLICY" to CommandHandler { CommandOutcome.Done() }),
                ack = acks::ack,
            ),
        )

        assertEquals(listOf(id(1)), queue.drain().done)
    }

    @Test
    fun `a failed fetch propagates rather than reporting an empty queue`() {
        val queue = CommandQueue(
            fetch = { throw IllegalStateException("no route to host") },
            executor = CommandExecutor(handlers = emptyMap(), ack = { _, _, _, _ -> }),
        )

        // An empty ExecutionReport here would read as "the queue was empty", which is the opposite
        // of what happened: nothing was handed over and the rows are still QUEUED.
        val thrown = runCatching { queue.drain() }.exceptionOrNull()
        assertEquals("no route to host", thrown?.message)
    }
}
