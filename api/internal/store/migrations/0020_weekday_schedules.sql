-- Weekday schedules: every Monday to Friday at a UTC time. weekday stays null for them, so
-- alerts_trigger_shape ((every = 'week') = (weekday is not null)) holds as is.
alter table alerts drop constraint alerts_every_check,
  add constraint alerts_every_check check (every in ('day','weekdays','week'));
