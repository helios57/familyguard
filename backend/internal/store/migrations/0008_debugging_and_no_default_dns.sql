-- Two changes to the policy row, both of them about giving a decision back to the parent.

-- 1. Developer options and adb, as a switch (FR-5.6).
--
-- `no_debugging_features` was unconditional from the first sync onwards, and it is the restriction
-- with the sharpest edge in the product: applying it as device owner switches adb off, the setting
-- survives a reboot, and with the control plane unreachable there is then no way into the phone at
-- all. That is the right default for a child's handset and the wrong one for the phone the person
-- building this is holding — which is the same phone, in this household, this month.
--
-- DEFAULT FALSE, so every existing row keeps exactly the behaviour it has today: the column is the
-- new way to say "and not this one", never a change of policy applied by a migration.
ALTER TABLE policies ADD COLUMN allow_debugging BOOLEAN NOT NULL DEFAULT FALSE;

-- 2. No DNS resolver by default.
--
-- The column shipped defaulting to a third-party filtering resolver, so every family that never
-- opened the setting had its children's queries pointed at it. Two reasons that is wrong, and the
-- second is the one that decided it: it is a filtering choice nobody made, and it does not do the
-- job people assume from the name — a DoT resolver cannot see, and therefore cannot remove, the
-- advertising served inside an app over the app's own TLS connection to its own backend. Owner,
-- 2026-09-06: "its not helping against AD's inside apps which are the most annoying."
--
-- An empty host is a defined state everywhere downstream, not a hole: `DnsPolicyManager` reads it as
-- OPPORTUNISTIC, which is encrypted DNS to whatever resolver the network offers rather than DNS
-- switched off. The phone is no worse off than a phone with no DPC on it.
--
-- Rows still holding the old default are moved; a host a parent typed themselves is left alone,
-- because this migration is undoing a default and not overruling a choice.
ALTER TABLE policies ALTER COLUMN dns_host SET DEFAULT '';
UPDATE policies SET dns_host = '', version = version + 1, updated_at = NOW()
 WHERE dns_host = 'family.adguard-dns.com';
