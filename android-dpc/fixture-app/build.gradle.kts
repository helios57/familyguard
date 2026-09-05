plugins {
    alias(libs.plugins.android.application)
}

// A real, installable, signed APK that is not this project's DPC — built twice, in two versions.
//
// It exists because the managed-install feature (FR-16) cannot be measured against anything else.
// Installing the DPC over itself is the self-update path and proves nothing about a *second*
// package; installing a system app fails for reasons that have nothing to do with policy; and a
// hand-assembled zip is not an APK the platform will parse. So the suite needs one package it owns
// end to end, and this is it.
//
// Deliberately empty of code. No Kotlin source set, no activity, no receiver: what is being
// measured is the installer, and a component here would only add ways for the fixture itself to
// fail.
//
// The two flavours differ in exactly one number — the one the applier compares — and are built in
// the same invocation, which `-P` properties cannot do: a property is per-build, so producing v1
// and v2 that way means two Gradle runs and a staging step that has to keep the first output alive
// across the second. `:app`'s `-PbuildOffset` predates this and stays as it is; it has a single
// caller that genuinely does want two separate builds.
android {
    namespace = "io.github.helios57.familyguard.fixture"
    compileSdk = 37
    compileSdkMinor = 1
    buildToolsVersion = "37.0.0"

    defaultConfig {
        applicationId = "io.github.helios57.familyguard.fixture"
        // Matches :app. A fixture that could not install on the floor would make an API 29 run
        // report a policy failure that was really a manifest one.
        minSdk = 29
        targetSdk = 37
    }

    flavorDimensions += "revision"
    productFlavors {
        create("v1") {
            dimension = "revision"
            versionCode = 1
            versionName = "0.0.1"
        }
        create("v2") {
            dimension = "revision"
            versionCode = 2
            versionName = "0.0.2"
        }
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_21
        targetCompatibility = JavaVersion.VERSION_21
    }
}

// AGP 9 has Kotlin support built in and puts kotlin-stdlib on the runtime classpath of every
// application module, whether or not the module has any Kotlin in it. Here that was 2.6 MB of dex
// in a fixture with no code at all — which then rides in the instrumentation APK and, as a
// checked-in parser fixture, in git. Excluded rather than tolerated: this module has to stay small
// enough that nobody is tempted to stop regenerating it.
configurations.configureEach {
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib")
}

// ---- handing the built APKs to :app's instrumented tests ---------------------------------------
//
// Named configurations rather than artifact attributes. `project(path, configuration)` picks one by
// name, so there is no attribute-matching step that can start selecting a different variant when
// AGP changes what it publishes — and the failure mode of attribute matching is a resolution error
// nobody can read. The artifact is AGP's APK *directory* (it holds output-metadata.json beside the
// file), which is why the consuming task looks inside it rather than expecting a single file.
val fixtureApkV1: Configuration by configurations.creating {
    isCanBeConsumed = true
    isCanBeResolved = false
}
val fixtureApkV2: Configuration by configurations.creating {
    isCanBeConsumed = true
    isCanBeResolved = false
}

androidComponents {
    onVariants { variant ->
        val target = when (variant.name) {
            "v1Debug" -> fixtureApkV1
            "v2Debug" -> fixtureApkV2
            else -> return@onVariants
        }
        artifacts.add(target.name, variant.artifacts.get(com.android.build.api.artifact.SingleArtifact.APK))
    }
}
