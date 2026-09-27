package io.github.helios57.familyguard.sync

/**
 * The digest of the last inventory the server accepted, bound to the device record that accepted it.
 *
 * [InventoryReporter] sends only when the list changed. That is only true relative to ONE server
 * record: a phone enrolled again — a new QR, a new device on the server — holds a digest that record
 * never saw. Measured 2026-09-27 on the emulator: after re-enrolment every sync said "unchanged", the
 * new record never received an app list, and a pause had nothing on the server's side to take.
 *
 * So the digest is stored as `<device id>:<digest>` and read back only for the same device id. A
 * digest stored before this existed carries no id and is not trusted, which costs each phone one
 * redundant inventory after the update that brings this.
 */
class InventoryDigest(
    private val read: () -> String?,
    private val write: (String) -> Unit,
    private val deviceId: () -> String,
) {
    fun last(): String {
        val id = deviceId()
        if (id.isEmpty()) return ""
        val stored = read().orEmpty()
        val prefix = "$id:"
        return if (stored.startsWith(prefix)) stored.removePrefix(prefix) else ""
    }

    fun record(digest: String) {
        val id = deviceId()
        if (id.isNotEmpty()) write("$id:$digest")
    }
}
