package io.github.helios57.familyguard.admin

import android.app.admin.DeviceAdminService

/**
 * Keeps the process alive as far as the platform allows a device owner's to be.
 *
 * An empty class is the whole implementation: `DeviceAdminService` exists so the system can bind the
 * owner app and treat it as persistent, and the manifest entry is what does the work. Without it,
 * an aggressive OEM task killer can stop the DPC and the phone quietly enforces nothing until the
 * next boot — a failure whose only symptom is that a bedtime does not happen.
 */
class AdminService : DeviceAdminService()
