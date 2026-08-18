#!/usr/bin/env python3
"""One assertion about a rendered manifest stream, chosen by name.

Split out of `render.sh` because each of these is a question about the *structure* of the YAML, and
a shell pipeline answering it with `grep` answers a question about the *text* — which is how a
`readOnly: true` belonging to some other volume comes to satisfy a check about the APK mount.

Exit 0 = the property holds. Exit 1 = it does not, and the reason is printed. Anything else is a
bug in this file, and the caller treats it as a failure rather than as a pass.
"""
import sys

try:
    import yaml
except ImportError:  # pragma: no cover - reported by the caller as NOT MEASURED
    print("NOT MEASURED: PyYAML is not installed", file=sys.stderr)
    sys.exit(2)


def load(path):
    with open(path, encoding="utf-8") as fh:
        return [d for d in yaml.safe_load_all(fh) if d]


def pod_templates(docs):
    """Every PodSpec in the stream, with a name that says where it came from.

    A CronJob buries its pod two levels deeper than a Deployment does, and a check that only walked
    `spec.template.spec` would silently pass over the backup job — reporting that "every pod runs
    as non-root" having examined every pod but that one.
    """
    for d in docs:
        kind, name = d.get("kind"), d.get("metadata", {}).get("name", "?")
        if kind in ("Deployment", "StatefulSet", "DaemonSet"):
            yield f"{kind}/{name}", d["spec"]["template"]["spec"]
        elif kind == "Job":
            yield f"{kind}/{name}", d["spec"]["template"]["spec"]
        elif kind == "CronJob":
            yield f"{kind}/{name}", d["spec"]["jobTemplate"]["spec"]["template"]["spec"]


def containers(pod):
    return list(pod.get("initContainers", [])) + list(pod.get("containers", []))


def fail(msg):
    print(f"        {msg}")
    sys.exit(1)


def find(docs, kind, name):
    for d in docs:
        if d.get("kind") == kind and d.get("metadata", {}).get("name") == name:
            return d
    fail(f"the render contains no {kind}/{name}")


def main():
    path, what = sys.argv[1], sys.argv[2]
    docs = load(path)
    if not docs:
        fail("the render contains no objects")

    if what == "expected-kinds":
        # A resource dropped from `kustomization.yaml` renders cleanly and deploys an incomplete
        # system: no Ingress is a site nobody can reach, no CronJob is a database nobody backs up,
        # and both render exactly as well as the whole thing does. Counted by kind rather than
        # listed by name so that renaming an object is a decision this file has to be told about
        # only when the shape changes.
        want = {"Namespace": 1, "Deployment": 2, "Service": 2, "Ingress": 1, "CronJob": 1}
        got = {}
        for d in docs:
            got[d.get("kind", "?")] = got.get(d.get("kind", "?"), 0) + 1
        for kind, n in sorted(want.items()):
            if got.get(kind, 0) != n:
                fail(f"expected {n} {kind}, rendered {got.get(kind, 0)}")
        return

    if what == "no-secrets":
        bad = [d["metadata"]["name"] for d in docs if d.get("kind") == "Secret"]
        if bad:
            fail(f"Secret rendered: {', '.join(bad)}")
        return

    if what == "nonroot":
        seen = 0
        for where, pod in pod_templates(docs):
            seen += 1
            uid = pod.get("securityContext", {}).get("runAsUser")
            for c in containers(pod):
                uid = c.get("securityContext", {}).get("runAsUser", uid)
                if uid is None:
                    fail(f"{where}/{c['name']} declares no runAsUser, so it runs as whatever the image says")
                if uid == 0:
                    fail(f"{where}/{c['name']} runs as uid 0")
        if seen == 0:
            fail("no pod templates found — this assertion examined nothing")
        return

    if what == "pinned":
        floating = {"latest", "edge", "main", "master", "stable"}
        seen = 0
        for where, pod in pod_templates(docs):
            for c in containers(pod):
                seen += 1
                image = c["image"]
                ref = image.rsplit("/", 1)[-1]
                if "@" in image:
                    continue  # pinned by digest, which is stronger than a tag
                if ":" not in ref:
                    fail(f"{where}/{c['name']} has no tag ({image}), which is an implicit :latest")
                if ref.rsplit(":", 1)[1] in floating:
                    fail(f"{where}/{c['name']} is on a floating tag ({image})")
        if seen == 0:
            fail("no containers found — this assertion examined nothing")
        return

    if what == "same-postgres":
        # pg_dump refuses to dump a server newer than itself, and a newer pg_dump can write an
        # archive an older pg_restore cannot read. Two tags that agree today are two tags that can
        # be bumped one at a time.
        db = find(docs, "Deployment", "familyguard-db")
        job = find(docs, "CronJob", "familyguard-db-backup")
        db_img = db["spec"]["template"]["spec"]["containers"][0]["image"]
        job_img = job["spec"]["jobTemplate"]["spec"]["template"]["spec"]["containers"][0]["image"]
        if db_img != job_img:
            fail(f"database runs {db_img} but the backup job runs {job_img}")
        return

    if what == "apk-mount-readonly":
        dep = find(docs, "Deployment", "familyguard-control-plane")
        pod = dep["spec"]["template"]["spec"]
        env = {e["name"]: e.get("value") for e in containers(pod)[0].get("env", [])}
        apk = env.get("APK_PATH")
        if not apk:
            fail("the control plane sets no APK_PATH, so nothing serves the DPC")
        mounts = containers(pod)[0].get("volumeMounts", [])
        owning = [m for m in mounts if apk.startswith(m["mountPath"].rstrip("/") + "/")]
        if not owning:
            fail(f"APK_PATH is {apk} but no volumeMount covers it")
        for m in owning:
            if not m.get("readOnly"):
                fail(f"the volume holding {apk} is mounted writable at {m['mountPath']}")
        return

    if what == "probes":
        dep = find(docs, "Deployment", "familyguard-control-plane")
        c = dep["spec"]["template"]["spec"]["containers"][0]
        for probe in ("startupProbe", "readinessProbe", "livenessProbe"):
            if probe not in c:
                fail(f"the control plane declares no {probe}")
        # A liveness probe on the same path as readiness would restart the pod for a database
        # outage; readiness must be the one that checks the dependency.
        if c["readinessProbe"].get("httpGet", {}).get("path") == c["livenessProbe"].get("httpGet", {}).get("path"):
            fail("readiness and liveness probe the same path, so a database outage restarts the pod")
        return

    if what == "db-recreate":
        db = find(docs, "Deployment", "familyguard-db")
        strategy = db["spec"].get("strategy", {}).get("type")
        if strategy != "Recreate":
            fail(f"database strategy is {strategy!r}; a rolling update starts a second writer on one volume")
        return

    if what == "backup-verifies":
        job = find(docs, "CronJob", "familyguard-db-backup")
        script = "\n".join(job["spec"]["jobTemplate"]["spec"]["template"]["spec"]["containers"][0].get("args", []))
        # Each needle names a DISTINCT use, because the loose form of this check could not tell
        # them apart: `pg_restore` alone is satisfied by the cheap `--list` parse, so deleting the
        # restore-into-a-database left this assertion green. The calibration caught it, which is
        # what a calibration is for; the fix is to say which invocation is meant.
        for needle, why in (
            ("pg_dump", "it does not dump"),
            ("pg_restore --list", "it never parses the archive, so a truncated file looks fine"),
            ("createdb", "it does not create a scratch database to restore into"),
            ("pg_restore --dbname=", "it never RESTORES the dump, so an unrestorable file would be kept as a backup"),
            ("--exit-on-error", "pg_restore reports errors and still exits 0, so the restore would prove nothing"),
            (".tmp", "it writes straight to the final name, so a failed dump becomes 'the backup'"),
        ):
            if needle not in script:
                fail(f"the backup job has no {needle}: {why}")
        # The rename must come after the restore, or the ordering that makes the whole thing
        # meaningful is gone while every keyword above is still present.
        if script.index("pg_restore") > script.index('mv "$tmp"'):
            fail("the dump is renamed into place before it is restored")
        return

    fail(f"unknown assertion {what!r}")


if __name__ == "__main__":
    main()
