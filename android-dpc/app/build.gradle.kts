import com.android.build.api.artifact.SingleArtifact
import com.android.build.api.variant.HostTestBuilder
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "io.github.helios57.familyguard"
    // 37.1 is the newest released platform AGP 9.3 supports (its documented maximum is API 37),
    // and androidx now floors at 37: appcompat 1.8.0 refuses to link against 35. The minor is
    // stated separately because the platform is `android-37.1`, not `android-37`.
    compileSdk = 37
    compileSdkMinor = 1
    buildToolsVersion = "37.0.0"

    defaultConfig {
        applicationId = "io.github.helios57.familyguard"
        // minSdk 29 (NFR-13), not the 26 the concept and REQUIREMENTS.md first named. Private DNS is set by
        // DevicePolicyManager.setGlobalPrivateDnsModeSpecifiedHost, which is API 29. On 26-28 the
        // call does not exist, so FR-6.1 cannot be met at all — and an app that installs there would
        // enforce every other control while silently leaving DNS filtering off. Refusing to install
        // is the honest behaviour; a runtime version check that logs and continues is not.
        minSdk = 29
        targetSdk = 37
        versionCode = 1
        versionName = "0.1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildFeatures {
        // BuildConfig.DEBUG decides one thing: whether an http:// control-plane URL may be accepted
        // for loopback and the emulator. A release build refuses it outright. Off by default in
        // AGP 8, and a build-time constant is the only form of that switch that cannot be flipped
        // on a shipped device.
        buildConfig = true
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
        release {
            // R8 off for now. A DPC is reached almost entirely through the framework — receivers,
            // services and activities named in the manifest, and DeviceAdminReceiver callbacks — so
            // shrinking it needs keep rules that have been tested on a real provisioning run, not
            // guessed. Turning it on before Phase 7 would trade a measurable app for an unmeasured
            // one.
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    // 17, while the JDK that runs the build is the newest there is. The two are different questions
    // and only one of them reaches the phone.
    //
    // The JDK is a build-time tool: measured on 2026-08-18, `assembleDebug`, the unit suite and
    // `assembleDebugAndroidTest` are all green under Temurin 26.0.2 and under 25.0.4, so CI pins the
    // newest. This number is the *bytecode* level, and the floor it has to clear is a Galaxy S20 —
    // Android 10, API 29. `dexBuilderDebug` accepts class files built at 21, so the build would go
    // green either way; a green build is simply not evidence for the thing that matters here, which
    // is whether the app still runs on API 29. That is what the instrumented layer measures, and it
    // has not been run on an API 29 device yet. Raising this before then would be choosing a number
    // over a measurement.
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    testOptions {
        unitTests {
            isReturnDefaultValues = false
            isIncludeAndroidResources = true
        }
    }

    packaging {
        resources.excludes += setOf("/META-INF/{AL2.0,LGPL2.1}")
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_17)
        // A deprecation warning on a DevicePolicyManager call means the platform changed the
        // contract under us, and that is exactly the class of change that shows up as a real phone
        // behaving differently from the emulator. It fails the build instead of scrolling past.
        allWarningsAsErrors.set(true)
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.material)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)

    testImplementation(libs.junit)
    testImplementation(libs.kotlinx.coroutines.test)

    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.espresso.core)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.rules)
}

// ---- the fixtures the unit tests read off their own classpath ----------------------------------
//
// Three files have to be on the unit-test classpath that are not checked in under `src/test`:
//
//   - backend/internal/policy/vectors.json — the single description of what the enforcement rules
//     mean. The Go engine replays it and so does EnforcementEngineVectorsTest, which is the only
//     thing that can catch the two implementations drifting apart, and drift here means the phone
//     enforcing a different bedtime from the one the parent is looking at.
//   - backend/internal/auth/recovery-vectors.json — the same arrangement for FR-12.3. The
//     consequence of drift is narrower and worse: a device that rejects the code printed on the
//     parent's screen, on a phone that is by then locked down and offline.
//   - the *merged* manifest. The one in src/main is not the one that ships: library manifests merge
//     into it and bring components of their own, so an exported-surface check that reads src/main is
//     a check of the half nobody would have got wrong, and it would stay green through a dependency
//     adding an unguarded exported receiver — which is the only realistic way one gets in.
//
// All three are staged by a task rather than read through a relative path, so that a moved or
// renamed source is a build failure. A test that reads ../../../backend/... would pass a refactor by
// finding nothing and asserting over zero vectors.
//
// They are wired in through `addGeneratedSourceDirectory`, which is the AGP 9 replacement for adding
// a directory to `android.sourceSets`. The replacement is not a rename: a source *directory* is a
// path, and a path does not carry its producer, so the previous arrangement needed the dependency
// stating by hand and the first version of it silently ran the tests against an empty directory.
// `addGeneratedSourceDirectory` takes the task, not the path, and infers the dependency — the whole
// class of failure goes away rather than being guarded against.

/**
 * Stages exactly one file into a directory of its own, under the name a test asks for.
 *
 * The input is a collection rather than a `RegularFileProperty` so that a *missing* source reaches
 * the task action instead of failing Gradle's own input validation first. That matters only for the
 * message: "the shared enforcement vectors are missing at <path>" sends the next person to the file
 * that moved, where "file specified for property 'source' does not exist" sends them here.
 */
@CacheableTask
abstract class StageTestFixture : DefaultTask() {
    @get:InputFiles
    @get:PathSensitive(PathSensitivity.NONE)
    abstract val source: ConfigurableFileCollection

    @get:Input
    abstract val resourceName: Property<String>

    @get:Input
    abstract val missingSourceMessage: Property<String>

    @get:OutputDirectory
    abstract val outputDirectory: DirectoryProperty

    @TaskAction
    fun stage() {
        val file = source.singleOrNull()?.takeIf(File::isFile)
            ?: throw GradleException(missingSourceMessage.get())
        val target = outputDirectory.get().asFile
        target.mkdirs()
        file.copyTo(target.resolve(resourceName.get()), overwrite = true)
    }
}

val policyVectorsSource = rootProject.layout.projectDirectory.file("../backend/internal/policy/vectors.json")
val recoveryVectorsSource =
    rootProject.layout.projectDirectory.file("../backend/internal/auth/recovery-vectors.json")

androidComponents {
    // Selected on the tested build type rather than applied to every variant. Unit tests exist for
    // that one only ("debug" by default), so an unselected loop would reach `release`, find no unit
    // test component, and need a null-skip — and that same skip would then quietly cover a `debug`
    // that had lost its unit tests, taking three fixtures off the classpath while every remaining
    // test still passed.
    onVariants(selector().withBuildType(android.testBuildType)) { variant ->
        val unitTest = variant.hostTests[HostTestBuilder.UNIT_TEST_TYPE]
            ?: throw GradleException("variant ${variant.name} has no unit test component; the shared vectors have nowhere to go")
        val testResources = unitTest.sources.resources
            ?: throw GradleException("variant ${variant.name}'s unit tests expose no Java-resource sources")

        val suffix = variant.name.replaceFirstChar { it.uppercase() }

        val policyVectors = tasks.register<StageTestFixture>("stage${suffix}PolicyVectors") {
            source.from(policyVectorsSource)
            resourceName.set("vectors.json")
            missingSourceMessage.set(
                "the shared enforcement vectors are missing at ${policyVectorsSource.asFile.absolutePath}; " +
                    "the Kotlin engine cannot be checked against the Go one without them"
            )
        }
        testResources.addGeneratedSourceDirectory(policyVectors, StageTestFixture::outputDirectory)

        val recoveryVectors = tasks.register<StageTestFixture>("stage${suffix}RecoveryVectors") {
            source.from(recoveryVectorsSource)
            resourceName.set("recovery-vectors.json")
            missingSourceMessage.set(
                "the shared recovery vectors are missing at ${recoveryVectorsSource.asFile.absolutePath}; " +
                    "the DPC's fold and derivation cannot be checked against the server's without them"
            )
        }
        testResources.addGeneratedSourceDirectory(recoveryVectors, StageTestFixture::outputDirectory)

        val mergedManifest = tasks.register<StageTestFixture>("stage${suffix}MergedManifest") {
            source.from(variant.artifacts.get(SingleArtifact.MERGED_MANIFEST))
            resourceName.set("MergedAndroidManifest.xml")
            missingSourceMessage.set(
                "the merged manifest for ${variant.name} was not produced; the exported-surface check " +
                    "would otherwise read the unmerged one in src/main and miss every library component"
            )
        }
        testResources.addGeneratedSourceDirectory(mergedManifest, StageTestFixture::outputDirectory)
    }
}
