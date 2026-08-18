package io.github.helios57.familyguard.commands

import io.github.helios57.familyguard.net.CommandId
import io.github.helios57.familyguard.net.DeviceCommand

/** What one handler did. There is no third state: a command either ran or it did not. */
sealed interface CommandOutcome {
    /** [result] is whatever the parent should be shown — a position fix, a lock timestamp. */
    data class Done(val result: Map<String, String> = emptyMap()) : CommandOutcome

    /** [reason] is what the console displays under the command. It is read by a person. */
    data class Failed(val reason: String) : CommandOutcome
}

/**
 * One command type, executed.
 *
 * A handler may not throw — but it is written by a person, so [CommandExecutor] catches anyway and
 * turns a throw into a [CommandOutcome.Failed]. The distinction matters for what the parent sees:
 * a caught throw is reported as the exception it was, never as "done".
 */
fun interface CommandHandler {
    fun handle(command: DeviceCommand): CommandOutcome
}

/** What one drain of the queue did, for the log and for the tests. */
data class ExecutionReport(
    /** Ids that ran and were acknowledged. */
    val done: List<String> = emptyList(),
    /** Ids that ran or were rejected, and were acknowledged as failed, with the reason. */
    val failed: Map<String, String> = emptyMap(),
    /**
     * Ids whose *acknowledgement* could not be delivered, with the transport's reason.
     *
     * Separate from [failed] because the two are opposite facts. A command in [failed] did not
     * happen and the parent knows. A command here may well have happened, and the console will show
     * it as delivered-never-acknowledged until the parent gives up on it. The device cannot fix that
     * by retrying: the server has already moved the row out of the queue, so a retry would be this
     * process guessing, and the honest end state is a console that says "no answer".
     */
    val unacknowledged: Map<String, String> = emptyMap(),
) {
    val ok: Boolean get() = failed.isEmpty() && unacknowledged.isEmpty()

    override fun toString(): String =
        if (ok) "commands done=${done.size}"
        else "commands done=${done.size} failed=$failed unacked=$unacknowledged"
}

/**
 * Runs the commands the server handed over, one at a time, and reports each one as it finishes.
 *
 * Three rules, and each of them is a decision about a child holding the phone:
 *
 * **In the order given.** The server returns oldest first, and a parent who pressed the alarm and
 * then pressed stop sent two commands that only mean anything in sequence. Executing them
 * concurrently, or newest first, ends with a siren nobody asked for and no second command left to
 * stop it.
 *
 * **Acknowledged immediately, never batched at the end.** The parent is watching the console while
 * this runs. Holding six acknowledgements until the last handler returns makes a `LOCATE_NOW` that
 * finished in two seconds invisible for as long as the slowest command in the same batch takes — and
 * if the process is killed in between, invisible forever, despite having run.
 *
 * **Every command is answered.** An unknown type is acknowledged as failed, naming the type, rather
 * than skipped: skipping is indistinguishable at the console from a phone that never woke up, and it
 * is the shape that hides a server that has been deployed ahead of the fleet. The one command that
 * is *not* executed is one whose id is not a UUID — because the acknowledgement is addressed by that
 * id, so it could be neither reported nor, for `TRIGGER_ALARM`, ever stopped.
 */
class CommandExecutor(
    private val handlers: Map<String, CommandHandler>,
    private val ack: (id: String, ok: Boolean, result: Map<String, String>, error: String) -> Unit,
    private val log: (String) -> Unit = {},
) {

    fun execute(commands: List<DeviceCommand>): ExecutionReport {
        val done = mutableListOf<String>()
        val failed = LinkedHashMap<String, String>()
        val unacknowledged = LinkedHashMap<String, String>()

        for (command in commands) {
            if (!CommandId.isValid(command.id)) {
                // Deliberately not executed and deliberately not acknowledged — there is no id to
                // acknowledge it with. Recorded here so the drain is not silently short.
                val reason = "command id is not a UUID, so it can never be acknowledged"
                unacknowledged[command.id] = reason
                log("refusing ${command.type}: $reason")
                continue
            }

            val handler = handlers[command.type]
            val outcome = if (handler == null) {
                CommandOutcome.Failed("this device does not implement command type '${command.type}'")
            } else {
                try {
                    handler.handle(command)
                } catch (e: Exception) {
                    // A handler reaches the platform, and the platform throws for reasons that are
                    // not bugs — a permission revoked by hand, a service that is gone. One command
                    // failing must not cost the batch the ones behind it.
                    CommandOutcome.Failed(e.message ?: e.javaClass.simpleName)
                }
            }

            val result = (outcome as? CommandOutcome.Done)?.result ?: emptyMap()
            val error = (outcome as? CommandOutcome.Failed)?.reason ?: ""
            try {
                ack(command.id, outcome is CommandOutcome.Done, result, error)
                if (outcome is CommandOutcome.Done) done += command.id else failed[command.id] = error
            } catch (e: Exception) {
                // The command ran. Only the report did not arrive, and the next command in the batch
                // is still worth running: a failed `STOP_ALARM` acknowledgement is not a reason to
                // leave the siren on.
                unacknowledged[command.id] = e.message ?: e.javaClass.simpleName
            }
        }

        return ExecutionReport(done, failed, unacknowledged)
    }
}

/**
 * The queue, end to end: take what the server is holding, then run it.
 *
 * [fetch] is bound in here rather than left to the caller because it is a *state change* and not a
 * read — `GET /device/commands` is what moves a row from QUEUED to DELIVERED (FR-9.1). A caller
 * holding the fetch itself is a caller that can "just check whether anything is waiting" and thereby
 * mark six commands delivered that nothing then executes. One fetch, one execution, one report.
 */
class CommandQueue(
    private val fetch: () -> List<DeviceCommand>,
    private val executor: CommandExecutor,
) {
    /**
     * @throws Exception when the fetch fails. Deliberately propagated: nothing was handed over, the
     * rows are still QUEUED, and the next drain will get them. An empty [ExecutionReport] here would
     * read as "the queue was empty", which is the opposite of what happened.
     */
    fun drain(): ExecutionReport = executor.execute(fetch())
}
