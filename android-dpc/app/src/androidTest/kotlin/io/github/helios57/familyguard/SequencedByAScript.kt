package io.github.helios57.familyguard

/**
 * Marks an instrumented class that must not run in an unsequenced sweep.
 *
 * Two classes here only mean anything in a particular position: [WipeableAsFoundTest] reads the
 * state a reboot left and has to run *after* one, and `ServerDrivenEnrollmentTest` needs a live
 * control plane and a single-use token that only `tests/android/self-update.sh` can hand it. Both
 * fail rather than skip when run out of position — deliberately, because a skip in the one layer
 * that can see the device is a green that measured nothing — so something has to keep them out of
 * the plain sweep, and `tests/android/instrumented.sh` is that something.
 *
 * **It is an annotation rather than a list of class names in the script because AGP splits its
 * instrumentation arguments on commas.** Measured 2026-09-05: passing
 * `-Pandroid.testInstrumentationRunnerArguments.notClass=A,B` made AGP emit
 * `am instrument … -e notClass A` — the second class became a malformed second argument and was
 * dropped in silence. `ServerDrivenEnrollmentTest` then ran in every sweep and failed every time, on
 * a filter that looked right in the script and in the runner (`am instrument -e notClass A,B` by
 * hand excludes both). One annotation is one value with no comma in it, so the argument cannot
 * narrow itself again however many classes end up wearing it.
 */
@Retention(AnnotationRetention.RUNTIME)
@Target(AnnotationTarget.CLASS)
annotation class SequencedByAScript
