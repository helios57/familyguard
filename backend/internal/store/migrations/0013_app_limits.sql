-- Approving an app and exempting it from every limit used to be the same decision (FR-5.4).
--
-- `action` was ALLOW or BLOCK, and with free installation off a newly installed app stays in
-- `pending_approval` — suspended — until it gets one of the two. But ALLOW is the whitelist: an
-- allowed app is exempt from bedtime and from the daily limit, permanently. So a parent who only
-- wanted to say "yes, you may have this" had to hand over an app no schedule could ever pause, and
-- the only other answer available was to leave it suspended forever.
--
-- That is not a hypothetical. Measured on the family phone on 2026-09-20: WhatsApp, Threema, Signal
-- and Audible were all installed after the device filed its first inventory, so all four sat
-- suspended in `pending_approval` waiting for an approval that could only be granted as a blanket
-- exemption. Turning bedtime off changed nothing, because bedtime was never what held them.
--
-- LIMIT is the missing third answer: approved, and governed like everything else — it counts
-- against the daily limit and it pauses at bedtime. `limit_minutes` rides on top of it: 0 means
-- only the family's daily limit applies, and a positive value is this one app's own daily
-- allowance, spent independently of the shared one.
--
-- The four answers a parent can now give a pending app, which is what the console offers:
--   ALLOW            — always free: no bedtime, no quota
--   LIMIT, 0         — daily limit: the normal case, governed like every other app
--   LIMIT, n         — individual limit: as above, and at most n minutes of its own per day
--   BLOCK            — always blocked: suspended and hidden
-- and no rule at all remains "undecided", which is what keeps it in pending_approval.
--
-- Nothing changes for an existing row: every one of them is ALLOW or BLOCK and keeps its meaning,
-- and DEFAULT 0 is the no-per-app-cap value. A migration is a new way to say something, never a
-- policy change applied to children whose parents did not ask for it.
ALTER TABLE app_rules DROP CONSTRAINT app_rules_action_check;
ALTER TABLE app_rules ADD CONSTRAINT app_rules_action_check
  CHECK (action IN ('ALLOW', 'BLOCK', 'LIMIT'));

-- Capped at a day. A limit above 1440 minutes cannot bind, so accepting one would only let the
-- console show a number that means "no limit" while reading as a limit.
ALTER TABLE app_rules ADD COLUMN limit_minutes INTEGER NOT NULL DEFAULT 0
  CHECK (limit_minutes >= 0 AND limit_minutes <= 1440);
