-- FR-5.8: a minute counts toward the daily limit only on an app the limit would pause. Which apps
-- those are follows from the rules and the inventory on the day, and both change, so a later chart
-- of the day reads what was decided while it was current: `counted` is set on every row of today's
-- usage each time the phone reports it, from the same resolution the phone obeys.
--
-- NULL is a row recorded before 0.6.38, when every app's minutes counted except the home screen,
-- System UI and FamilyGuard itself; the console reads such a row by that rule, which is how the day
-- was actually enforced.
ALTER TABLE usage_samples ADD COLUMN counted BOOLEAN;
