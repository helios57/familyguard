package io.github.helios57.familyguard.recovery

import android.content.Context
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.InputMethodManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import io.github.helios57.familyguard.R
import io.github.helios57.familyguard.status.DeviceStatus
import io.github.helios57.familyguard.status.StatusLevel
import io.github.helios57.familyguard.status.StatusLine
import io.github.helios57.familyguard.status.deviceStatus
import io.github.helios57.familyguard.status.deviceStatusFacts
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * The offline escape hatch (FR-12): a text field, a button, and a line of status.
 *
 * This is the whole user interface of the DPC, and the *only* component of it a person can start.
 * It exists because every other way out of a managed phone needs something that may not be there —
 * the network, the console, a parent's laptop — and the situation it is for is the one where none
 * of those are. So it is a launcher entry rather than a deep link or a notification tap: the
 * notification only exists while the service is running, and a dead service is exactly when someone
 * needs this.
 *
 * Four things it deliberately does not do, each of which would undo the point of it:
 *
 * **It reads no Intent extras, and holds no state of its own.** Everything it decides comes from
 * [androidRecoveryController], which reads the encrypted stores. An activity that accepted, say, a
 * `skip_lockout` extra would be an exported component with a bypass in it — and this one is exported
 * by construction, because a launcher entry cannot be anything else. `ManifestAndPlatformCallsTest`
 * asserts the absence rather than trusting this paragraph.
 *
 * **It does not start `ConnectionService`.** Starting it would sync, and a sync that reaches the
 * server ends the recovery it was just granted — on a phone with signal, within seconds of the
 * parent letting go of it. The release ends when the *server* is next reached, which is the parent
 * changing something in the console, not this screen deciding to check.
 *
 * **It does no work on the main thread.** Opening the encrypted stores touches the keystore and
 * verifying a code is 120 000 rounds of PBKDF2 — around a fifth of a second on a cheap handset, and
 * an ANR waiting for the phone that is already having a bad day. The cost is deliberate; the
 * blocking is not.
 *
 * **It does not tell a wrong guess anything.** The rejection message is the same whichever part of
 * the code was wrong. The failure *count* is shown, because that is the parent's signal that
 * somebody else has been trying — and a child who reads it learns only that they are being counted.
 *
 * It uses its own [CoroutineScope] rather than `lifecycleScope`, which would mean a dependency this
 * app does not carry (see `app/build.gradle.kts`); the scope is cancelled in [onDestroy], so a
 * verification in flight when the screen closes cannot come back to a dead view.
 */
class RecoveryActivity : AppCompatActivity() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    private lateinit var code: EditText
    private lateinit var submit: Button
    private lateinit var status: TextView
    private lateinit var statusLoading: TextView
    private lateinit var statusLines: LinearLayout

    /**
     * Built once, off the main thread, and only when the screen is actually shown.
     *
     * Assigned and read on the main dispatcher only, so there is no lock: every coroutine here runs
     * on `Dispatchers.Main.immediate` and hops to IO for the blocking part.
     */
    private var controller: RecoveryController? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_recovery)

        code = findViewById(R.id.recovery_code)
        submit = findViewById(R.id.recovery_submit)
        status = findViewById(R.id.recovery_status)
        statusLoading = findViewById(R.id.status_loading)
        statusLines = findViewById(R.id.status_lines)

        submit.setOnClickListener { onSubmit() }
    }

    override fun onStart() {
        super.onStart()
        // Re-asked on every start rather than cached across one: a device can finish enrolling, or
        // be released by the parent from the console, while this screen sits in the background.
        scope.launch {
            val state = withContext(Dispatchers.IO) {
                val current = controller ?: androidRecoveryController(this@RecoveryActivity)
                controller = current
                Opening(
                    available = current.available(),
                    released = current.released(),
                    lockout = current.status(),
                )
            }
            render(state)
        }
        refreshStatus()
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun onSubmit() {
        val entered = code.text?.toString().orEmpty()
        if (entered.isBlank()) {
            status.text = getString(R.string.recovery_empty)
            return
        }

        // Disabled for the duration, because two submissions of the same code would journal two
        // attempts and — on a wrong one — burn two of the parent's free tries on one mistake.
        submit.isEnabled = false
        status.text = getString(R.string.recovery_checking)
        hideKeyboard()

        scope.launch {
            val result = withContext(Dispatchers.IO) {
                val current = controller ?: androidRecoveryController(this@RecoveryActivity)
                controller = current
                current.submit(entered)
            }
            render(result)
        }
    }

    /** What the screen knows before anything has been typed. */
    private data class Opening(
        val available: Boolean,
        val released: Boolean,
        val lockout: LockoutStatus,
    )

    private fun render(state: Opening) {
        when {
            !state.available -> {
                // The reason belongs to the controller, so the screen and a submitted code give the
                // same answer instead of two differently-worded ones.
                status.text = getString(
                    R.string.recovery_unavailable,
                    "this phone has no recovery code yet — finish enrolling it, or recover it from " +
                        "the family settings",
                )
                setEntryEnabled(false)
            }

            state.released -> {
                status.text = getString(R.string.recovery_already_released)
                setEntryEnabled(false)
            }

            state.lockout is LockoutStatus.Closed -> {
                status.text = getString(
                    R.string.recovery_locked_out,
                    formatWait(state.lockout.remainingMillis),
                )
                // Left enabled: the wait is checked again on submit, so the worst a press can do is
                // repeat the message — and a greyed-out button with no countdown reads as broken.
                setEntryEnabled(true)
            }

            else -> {
                status.text = ""
                setEntryEnabled(true)
            }
        }
    }

    private fun render(result: RecoveryResult) {
        submit.isEnabled = true
        when (result) {
            is RecoveryResult.Released -> {
                status.text = if (result.outcome.ok) {
                    getString(R.string.recovery_released)
                } else {
                    // The problems are shown, not swallowed. A phone that cleared its restrictions
                    // but not its app suspensions is still a phone the child cannot use, and a
                    // "done" that hides that sends the parent looking in the wrong place.
                    getString(
                        R.string.recovery_released_with_problems,
                        result.outcome.problems.values.joinToString("; "),
                    )
                }
                code.text?.clear()
                setEntryEnabled(false)
                // The release just changed two of the lines below — the rules are off now, and the
                // attempt that did it is queued and unreported. A status block still describing the
                // phone as managed, directly under a message saying it is not, is the screen
                // contradicting itself at the moment somebody is reading it hardest.
                refreshStatus()
            }

            is RecoveryResult.Rejected -> {
                val wait = result.wait
                status.text = if (wait is LockoutStatus.Closed) {
                    getString(
                        R.string.recovery_rejected_wait,
                        result.consecutiveFailures,
                        formatWait(wait.remainingMillis),
                    )
                } else {
                    resources.getQuantityString(
                        R.plurals.recovery_rejected,
                        result.consecutiveFailures,
                        result.consecutiveFailures,
                    )
                }
                code.text?.clear()
            }

            is RecoveryResult.TooManyAttempts ->
                status.text = getString(R.string.recovery_locked_out, formatWait(result.remainingMillis))

            is RecoveryResult.Unavailable -> {
                status.text = getString(R.string.recovery_unavailable, result.reason)
                setEntryEnabled(false)
            }
        }
    }

    /**
     * Gathers the facts off the main thread and draws them (FR-13.4).
     *
     * Its own coroutine rather than a second half of the one in [onStart], so the recovery controls
     * come alive as soon as the stores open. The status block reads three encrypted files and asks
     * the usage-stats service; making the button wait for that would mean the escape hatch is slower
     * than the report about it, on the screen whose entire reason for existing is the hatch.
     *
     * A failure to gather anything at all becomes a line saying so. There is no path here that
     * leaves the previous rows on screen next to a newer heading, and none that crashes: this is the
     * screen a parent opens when the phone is already misbehaving.
     */
    private fun refreshStatus() {
        scope.launch {
            val computed = withContext(Dispatchers.IO) {
                runCatching { deviceStatus(deviceStatusFacts(this@RecoveryActivity)) }.getOrNull()
            }
            renderStatus(computed)
        }
    }

    private fun renderStatus(computed: DeviceStatus?) {
        statusLoading.visibility = View.GONE
        statusLines.removeAllViews()
        if (computed == null) {
            statusLoading.text = getString(R.string.status_unavailable)
            statusLoading.visibility = View.VISIBLE
            return
        }
        val inflater = LayoutInflater.from(this)
        for (line in computed.lines) statusLines.addView(rowFor(inflater, statusLines, line))
    }

    /**
     * One row.
     *
     * A non-OK value gets three separate signals — the flag glyph, the error colour, and the words
     * "Needs attention" in the row's spoken description. Redundant on purpose: colour alone fails
     * for a colour-blind parent and in direct sunlight, and a glyph alone is invisible to a screen
     * reader.
     *
     * [StatusLevel.NOT_MEASURED] is drawn exactly like [StatusLevel.ATTENTION] and not more calmly.
     * The two mean different things and the value text says which, but a phone that cannot measure
     * screen time is not in a better state than one whose policy is stale — it is in a state where
     * nobody knows, and drawing that as a muted aside is how it stays unnoticed for a month.
     */
    private fun rowFor(inflater: LayoutInflater, parent: ViewGroup, line: StatusLine): View {
        val row = inflater.inflate(R.layout.status_row, parent, false)
        row.findViewById<TextView>(R.id.status_row_label).text = line.label

        val value = row.findViewById<TextView>(R.id.status_row_value)
        val fine = line.level == StatusLevel.OK
        value.text = if (fine) line.value else "${getString(R.string.status_flag)} ${line.value}"
        value.setTextColor(
            colorFor(if (fine) android.R.attr.textColorPrimary else androidx.appcompat.R.attr.colorError)
        )
        row.contentDescription = getString(
            if (fine) R.string.status_row_description else R.string.status_row_description_attention,
            line.label,
            line.value,
        )
        return row
    }

    /** Resolves a theme colour attribute. Literal colours would be unreadable in the other theme. */
    private fun colorFor(attribute: Int): Int {
        val typed = android.util.TypedValue()
        theme.resolveAttribute(attribute, typed, true)
        return if (typed.resourceId != 0) getColor(typed.resourceId) else typed.data
    }

    private fun setEntryEnabled(enabled: Boolean) {
        code.isEnabled = enabled
        submit.isEnabled = enabled
    }

    /**
     * A wait, rounded *up*, in whole minutes or whole seconds.
     *
     * Up, because the alternative tells a parent to come back in 9 minutes for a 9-minute-59-second
     * wait, and the press that follows is refused — which reads as the screen having lied. Rounding
     * up is only ever early by less than a unit.
     */
    private fun formatWait(millis: Long): String {
        val seconds = ceilDiv(millis, TimeUnit.SECONDS.toMillis(1))
        if (seconds < 60) {
            val whole = seconds.toInt().coerceAtLeast(1)
            return resources.getQuantityString(R.plurals.recovery_wait_seconds, whole, whole)
        }
        val minutes = ceilDiv(seconds, 60).toInt()
        return resources.getQuantityString(R.plurals.recovery_wait_minutes, minutes, minutes)
    }

    private fun hideKeyboard() {
        val manager = getSystemService(Context.INPUT_METHOD_SERVICE) as? InputMethodManager ?: return
        manager.hideSoftInputFromWindow(code.windowToken, 0)
    }

    private companion object {
        fun ceilDiv(value: Long, by: Long): Long = (value + by - 1) / by
    }
}
