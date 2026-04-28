package cron

import "errors"

// TriggerByName fires every entry whose Name matches name. Returns the
// successful dispatch count and errors.Join of per-Trigger failures.
// No match returns (0, nil); not running returns (0, ErrSchedulerNotRunning).
func (c *Cron) TriggerByName(name string) (int, error) {
	if !c.running.Load() {
		return 0, ErrSchedulerNotRunning
	}
	c.mu.Lock()
	var ids []EntryID
	for id, e := range c.byEntry {
		if e.name == name {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()

	count := 0
	var errs []error
	for _, id := range ids {
		if err := c.Trigger(id); err != nil {
			errs = append(errs, err)
			continue
		}
		count++
	}
	return count, errors.Join(errs...)
}
