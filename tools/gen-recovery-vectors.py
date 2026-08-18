#!/usr/bin/env python3
"""Generates backend/internal/auth/recovery-vectors.json.

Run from the familyguard root:  python3 tools/gen-recovery-vectors.py

Why a generator, and why in Python:

The `derive` expectations are PBKDF2-HMAC-SHA256 digests, which cannot be written by hand. Taking
them from the Go implementation would make the vectors assert only that Go does what Go does — and
the whole point of the file is to be a third party the Go and Kotlin sides are both measured
against. Python's hashlib is an independent implementation of the same RFC 8018 primitive, so a
digest produced here is evidence about the primitive rather than about either of the two mirrors.

Everything that CAN be written by hand is written by hand:

  * every `normalize` expectation is a literal below, derived from the rule in tokens.go's doc
    comment, never from running the code;
  * every `derive` vector carries its canonical form as a literal too, and this script hashes THAT.
    So a vector asserts "normalising this typed string gives that canonical string, and PBKDF2 over
    the canonical string gives this digest" — and the script never implements a normaliser, which
    would have been a third one to keep in step.

The codes and salts are deliberately low-entropy and obviously fake. They are committed, so they
must never be mistakable for a real credential.
"""

import base64
import hashlib
import json
import sys
from pathlib import Path

OUT = Path("backend/internal/auth/recovery-vectors.json")

SALT_A = b"vector-salt-0001"
SALT_B = b"vector-salt-0002"


def b64(raw: bytes) -> str:
    """base64.RawURLEncoding — URL alphabet, no padding, as the Go server emits."""
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


# --- normalize: raw in, canonical out. Every expectation hand-written from the rule. ------------
#
# The rule (tokens.go): uppercase every rune with the SIMPLE Unicode mapping, then drop every rune
# that is Unicode whitespace or one of the enumerated dashes. Nothing else is removed, and nothing
# is added.
NORMALIZE = [
    (
        "a code in the form the console prints it",
        "2345-6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        "lower case, because a phone keyboard starts there",
        "2345-6789-abcd-efgh-jkmn",
        "23456789ABCDEFGHJKMN",
    ),
    (
        "spaces where the console printed dashes",
        "2345 6789 ABCD EFGH JKMN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        "no separators at all",
        "23456789ABCDEFGHJKMN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        "leading and trailing whitespace, including a newline from a paste",
        "\n  2345-6789-ABCD-EFGH-JKMN \t\n",
        "23456789ABCDEFGHJKMN",
    ),
    (
        "a tab in the middle, which is what a copied table cell leaves behind",
        "2345\t6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # Go's unicode.IsSpace includes U+00A0; java.lang.Character.isWhitespace does NOT, so a
        # Kotlin mirror written with String.trim() or Char.isWhitespace() fails this one.
        "a non-breaking space at each end and in the middle, as a chat app pastes it",
        "\u00a02345-6789-ABCD-EFGH-JKM\u00a0N\u00a0",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # The complete set of characters Go calls whitespace and java.lang.Character.isWhitespace
        # does NOT: the three no-break spaces and the next-line control. U+00A0 has its own vector
        # above, for placement; this one is about coverage. A Kotlin mirror written with
        # Char.isWhitespace() or String.trim() passes every other normalize vector in this file and
        # fails exactly here, which is the only reason the mirror spells Go's set out by hand.
        "the four spaces Go strips that Java does not call whitespace at all",
        "\u00852345\u00a06789\u2007ABCD\u202fEFGH-JKMN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # The remainder of Go's whitespace set, which both sides agree on. Here because the mirror
        # expresses U+2000..U+200A as a RANGE beside its explicit set: both endpoints appear below,
        # so an off-by-one in that range is a red vector rather than a character the phone keeps and
        # the server drops.
        "the exotic spaces both platforms strip, including both ends of the U+2000 run",
        "2345\u000b6789\u000cABCD\rEFGH\u1680JK"
        "\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2008\u2009\u200a"
        "M\u2028\u2029\u205f\u3000N",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # The reverse asymmetry: Character.isWhitespace includes U+001C..U+001F, Go's IsSpace does
        # not. Neither side may strip it, so it survives, the code does not verify, and — the part
        # that matters — both sides say so. The expectation is therefore the surviving character
        # rather than a rejection: a mirror that used the Java predicate would fold it away and
        # would fail here, which is the point.
        "an ASCII file separator survives, because Go does not call it a space",
        "2345-6789-ABCD-EFGH-JKM\u001cN",
        "23456789ABCDEFGHJKM\u001cN",
    ),
    (
        # Six of the eleven dashes the two sides enumerate.
        "en dash, em dash, non-breaking hyphen, minus sign, fullwidth and an invisible soft hyphen",
        "2345\u20136789\u2014ABCD\u2011EFGH\u2212JKMN\uff0d\u00ad",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # The other five, so no entry in either list is left unexercised.
        "hyphen, figure dash, horizontal bar, small em dash and small hyphen-minus",
        "2345\u20106789\u2012ABCD\u2015EFGH\ufe58JK\ufe63MN",
        "23456789ABCDEFGHJKMN",
    ),
    (
        # String.uppercase() in Kotlin does FULL case mapping and turns this into "SS". Go's
        # strings.ToUpper does simple mapping and leaves it alone. Not a character any generated
        # code contains — it is here so the mirror stays pinned to the simple mapping.
        "a sharp s stays one character, because both sides map case simply",
        "2345-6789-ABCD-EFGH-JKM\u00df",
        "23456789ABCDEFGHJKM\u00df",
    ),
    (
        # U+0131 uppercases to "I" under the simple mapping on both sides. Here because the
        # alphabet deliberately has no I: a fold that invents one produces a code no device holds,
        # and the two sides must at least invent the same one.
        "a dotless i folds to I, which the alphabet does not contain — and that is still the fold",
        "2345-6789-ABCD-EFGH-JKM\u0131",
        "23456789ABCDEFGHJKMI",
    ),
    (
        "an empty string normalises to an empty string rather than throwing",
        "",
        "",
    ),
    (
        "a string of nothing but separators normalises away entirely",
        " - \t \u2013 \u00a0",
        "",
    ),
]

# --- derive: (name, code as typed, canonical form BY HAND, salt, iterations) --------------------
DERIVE = [
    (
        "the canonical code at a work factor of one, the lowest the server accepts",
        "2345-6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
        SALT_A,
        1,
    ),
    (
        "the same code at a thousand rounds: the digest must change with the work factor",
        "2345-6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
        SALT_A,
        1000,
    ),
    (
        "the same code and work factor under a different salt",
        "2345-6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
        SALT_B,
        1000,
    ),
    (
        # The one vector at the production work factor. It costs both suites about a fifth of a
        # second and it is the only evidence that the number a device actually runs is the number
        # both sides agree on — the cheap vectors above would all still pass if RecoveryIterations
        # were misread somewhere.
        "the production work factor, so the number that ships is the number that is checked",
        "2345-6789-ABCD-EFGH-JKMN",
        "23456789ABCDEFGHJKMN",
        SALT_A,
        120000,
    ),
    (
        # Same expected digest as the first vector, reached from a differently formatted input.
        # This is the vector that makes normalisation part of the cross-language contract rather
        # than an implementation detail each side may fold its own way.
        "a messily typed code derives to the same digest as the canonical one",
        "\u00a0 2345 6789\u2013abcd\u00adEFGH-jkmn\n",
        "23456789ABCDEFGHJKMN",
        SALT_A,
        1,
    ),
    (
        "a different code, so the digest is not a constant",
        "PQRS-TVWX-YZ23-4567-89AB",
        "PQRSTVWXYZ23456789AB",
        SALT_A,
        1,
    ),
]


def main() -> int:
    if not OUT.parent.is_dir():
        print(f"run this from the familyguard root: {OUT.parent} does not exist", file=sys.stderr)
        return 2

    normalize = [{"name": n, "raw": raw, "expect": exp} for n, raw, exp in NORMALIZE]

    derive = []
    for name, code, canonical, salt, iterations in DERIVE:
        digest = hashlib.pbkdf2_hmac("sha256", canonical.encode("utf-8"), salt, iterations, 32)
        derive.append(
            {
                "name": name,
                "code": code,
                "salt": b64(salt),
                "iterations": iterations,
                "expect_hash": b64(digest),
            }
        )

    # The verify cases reuse the first derive vector's material, so a change to one cannot leave
    # the other asserting against a digest nothing produces.
    good = derive[0]

    # A device whose stored hash is the digest of the EMPTY string. Read back out of the vector above
    # rather than restated, so the two cannot drift apart if DERIVE[0] is ever edited.
    #
    # This is what makes the empty-fold refusal load-bearing, and it was added because calibration
    # said it was not: deleting `NormalizeRecoveryCode(code) == ""` from VerifyRecoveryCode left every
    # other vector green. Against a real code's digest, an empty derivation simply does not match, so
    # the comparison rescues the missing guard and the break reads as "the test does not bind". Here
    # the comparison cannot rescue anything — without the guard, typing nothing unlocks the device.
    # Reaching this state takes a bug upstream, which is precisely when a last line of defence is the
    # only thing left standing.
    #
    # Deliberately a `verify` vector and not a `derive` one: Go's pbkdf2 hashes an empty password
    # happily, while SunJCE's PBKDF2KeyImpl refuses one outright, so a derive vector over "" would be
    # red on the JVM for a reason that says nothing about whether the mirror agrees.
    good_salt = base64.urlsafe_b64decode(good["salt"] + "=" * (-len(good["salt"]) % 4))
    empty_hash = b64(hashlib.pbkdf2_hmac("sha256", b"", good_salt, good["iterations"], 32))
    verify = [
        {
            "name": "the right code verifies",
            "code": good["code"],
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": True,
        },
        {
            "name": "a differently formatted right code verifies",
            "code": "\u00a0 2345 6789\u2013abcd\u00adEFGH-jkmn\n",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": True,
        },
        {
            "name": "a wrong code does not verify",
            "code": "PQRS-TVWX-YZ23-4567-89AB",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "the right code under the wrong salt does not verify",
            "code": good["code"],
            "salt": b64(SALT_B),
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "the right code at the wrong work factor does not verify",
            "code": good["code"],
            "salt": good["salt"],
            "iterations": 2,
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "a zero work factor denies rather than deriving something",
            "code": good["code"],
            "salt": good["salt"],
            "iterations": 0,
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "an absent hash denies; a device with no material never unlocks",
            "code": good["code"],
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": "",
            "expect": False,
        },
        {
            "name": "an absent salt denies",
            "code": good["code"],
            "salt": "",
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "an empty entry denies rather than deriving from an empty password",
            "code": "",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "an entry of nothing but separators denies for the same reason",
            "code": " -- \t ",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": good["expect_hash"],
            "expect": False,
        },
        {
            "name": "an empty entry denies even against material derived from the empty string",
            "code": "",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": empty_hash,
            "expect": False,
        },
        {
            "name": "an entry of nothing but separators denies against that material too",
            "code": "\u2013 -- \u00a0",
            "salt": good["salt"],
            "iterations": good["iterations"],
            "hash": empty_hash,
            "expect": False,
        },
    ]

    doc = {
        "_comment": [
            "Shared recovery-code vectors (FR-12.3). Replayed by backend/internal/auth (Go) and by",
            "the Android DPC's RecoveryVectorsTest (Kotlin). Two implementations that disagree about",
            "what a typed code IS produce a phone that rejects the code printed on the parent's",
            "screen, offline, with no way to debug it. That is the failure this file exists to",
            "prevent, so neither side may edit it to make its own implementation pass.",
            "",
            "The digests were produced by Python's hashlib.pbkdf2_hmac, a third implementation of",
            "RFC 8018, independent of both mirrors. The normalisation expectations were written by",
            "hand from the rule in tokens.go, never generated from an implementation's output.",
            "",
            "Every code and salt here is deliberately low-entropy and fake. Nothing in this file is,",
            "or resembles, a credential for any device.",
            "",
            "Regenerate with tools/gen-recovery-vectors.py, but note that regenerating is only",
            "correct for ADDING vectors. A digest that changed under an edit is the two sides being",
            "told to agree on something new, which is a decision, not a refresh.",
        ],
        "normalize": normalize,
        "derive": derive,
        "verify": verify,
    }

    # ensure_ascii, deliberately: half the interesting inputs here are a non-breaking space, a soft
    # hyphen or a file separator, and written as themselves they are a line no reviewer can check
    # and a substitution no diff would show. Escaped, the file says what it contains. Go's
    # encoding/json and kotlinx.serialization both decode \\uXXXX, so nothing is lost.
    OUT.write_text(json.dumps(doc, indent=2, ensure_ascii=True) + "\n", encoding="utf-8")
    print(f"wrote {OUT}: {len(normalize)} normalize, {len(derive)} derive, {len(verify)} verify")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
