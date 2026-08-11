package dbmaint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ScheduleMode is what an automatic run does.
type ScheduleMode string

const (
	// ModeIncremental returns free pages to the filesystem in bounded chunks.
	// Cheap, no window, no restart.
	ModeIncremental ScheduleMode = "incremental"
	// ModeFull rebuilds the database: a read-only window and a restart.
	ModeFull ScheduleMode = "full"
)

// Source labels where a resolved value came from, mirroring runtimecfg so the
// dashboard can render the same pills. It matters most for compose-configured
// servers: without it an admin sees automation they never enabled in the UI
// and has no way to find out why.
const (
	SourceDB          = "db"
	SourceEnv         = "env"
	SourceRecommended = "recommended"
)

// Schedule field names, used as keys in Schedule.Source.
const (
	FieldEnabled         = "enabled"
	FieldMode            = "mode"
	FieldIntervalHours   = "interval_hours"
	FieldMinFreePercent  = "min_free_percent"
	FieldMinFreeBytes    = "min_free_bytes"
	FieldWindowStartHour = "window_start_hour"
	FieldWindowEndHour   = "window_end_hour"
)

var ErrInvalidSchedule = errors.New("invalid schedule")

// ScheduleEnv is the env-var layer, read from config at wiring time. Nil
// pointers mean the variable was not set.
type ScheduleEnv struct {
	Enabled         *bool
	Mode            *string
	IntervalHours   *int
	MinFreePercent  *int
	MinFreeBytes    *int64
	WindowStartHour *int
	WindowEndHour   *int
}

// Schedule is the fully-resolved automatic-reclaim configuration plus the
// outcome of the last run.
//
// The outcome fields are read from the journal, not from the table. They
// cannot live in the database: a full run replaces the database, and nothing
// written while it runs survives into the new file.
type Schedule struct {
	Enabled         bool         `json:"enabled"`
	Mode            ScheduleMode `json:"mode"`
	IntervalHours   int          `json:"interval_hours"`
	MinFreePercent  int          `json:"min_free_percent"`
	MinFreeBytes    int64        `json:"min_free_bytes"`
	WindowStartHour *int         `json:"window_start_hour"`
	WindowEndHour   *int         `json:"window_end_hour"`

	// Configured is false when no admin has ever saved a schedule. The values
	// are then derived from the database's own auto-vacuum mode, which is how
	// an upgraded server comes up with automation off.
	Configured bool `json:"configured"`

	LastRunAt      *time.Time `json:"last_run_at"`
	LastStatus     string     `json:"last_status,omitempty"`
	LastFreedBytes *int64     `json:"last_freed_bytes"`
	LastError      string     `json:"last_error,omitempty"`

	Source map[string]string `json:"source"`
}

// NullableInt distinguishes "leave alone" from "clear" for the window bounds,
// which are the only nullable fields an admin can set.
type NullableInt struct {
	Set   bool
	Value *int
}

// SchedulePatch carries a change. Nil fields are left as they were.
type SchedulePatch struct {
	Enabled         *bool
	Mode            *string
	IntervalHours   *int
	MinFreePercent  *int
	MinFreeBytes    *int64
	WindowStartHour NullableInt
	WindowEndHour   NullableInt
}

// recommendedSchedule is the default when nothing has been configured.
//
// It is a function of the database's own mode rather than a constant. A file
// created by a recent build can already reclaim incrementally, so daily
// reclaim is free and it is switched on. A database carried over from an
// older install cannot, and the only thing automation could do for it is the
// expensive rebuild — so it stays off until an admin asks. An upgrade must
// never start blocking somebody's server on its own.
func recommendedSchedule(mode AutoVacuum) Schedule {
	return Schedule{
		Enabled:        mode == AutoVacuumIncremental,
		Mode:           ModeIncremental,
		IntervalHours:  24,
		MinFreePercent: recommendPercent,
		MinFreeBytes:   recommendBytes,
	}
}

type scheduleRow struct {
	enabled         sql.NullInt64
	mode            sql.NullString
	intervalHours   sql.NullInt64
	minFreePercent  sql.NullInt64
	minFreeBytes    sql.NullInt64
	windowStartHour sql.NullInt64
	windowEndHour   sql.NullInt64
}

func loadScheduleRow(ctx context.Context, sdb *sql.DB) (scheduleRow, bool, error) {
	var r scheduleRow
	err := sdb.QueryRowContext(ctx, `
		SELECT enabled, mode, interval_hours, min_free_percent, min_free_bytes,
		       window_start_hour, window_end_hour
		  FROM maintenance_settings WHERE id = 1`).
		Scan(&r.enabled, &r.mode, &r.intervalHours, &r.minFreePercent, &r.minFreeBytes,
			&r.windowStartHour, &r.windowEndHour)
	if errors.Is(err, sql.ErrNoRows) {
		return scheduleRow{}, false, nil
	}
	if err != nil {
		return scheduleRow{}, false, fmt.Errorf("select maintenance_settings: %w", err)
	}
	return r, true, nil
}

// ScheduleFor resolves the effective schedule: db → env → recommended.
func ScheduleFor(ctx context.Context, sdb *sql.DB, env ScheduleEnv, mode AutoVacuum, journal State) (Schedule, error) {
	out := recommendedSchedule(mode)
	out.Source = map[string]string{
		FieldEnabled: SourceRecommended, FieldMode: SourceRecommended,
		FieldIntervalHours: SourceRecommended, FieldMinFreePercent: SourceRecommended,
		FieldMinFreeBytes: SourceRecommended, FieldWindowStartHour: SourceRecommended,
		FieldWindowEndHour: SourceRecommended,
	}

	if env.Enabled != nil {
		out.Enabled, out.Source[FieldEnabled] = *env.Enabled, SourceEnv
	}
	if env.Mode != nil {
		out.Mode, out.Source[FieldMode] = ScheduleMode(*env.Mode), SourceEnv
	}
	if env.IntervalHours != nil {
		out.IntervalHours, out.Source[FieldIntervalHours] = *env.IntervalHours, SourceEnv
	}
	if env.MinFreePercent != nil {
		out.MinFreePercent, out.Source[FieldMinFreePercent] = *env.MinFreePercent, SourceEnv
	}
	if env.MinFreeBytes != nil {
		out.MinFreeBytes, out.Source[FieldMinFreeBytes] = *env.MinFreeBytes, SourceEnv
	}
	if env.WindowStartHour != nil {
		v := *env.WindowStartHour
		out.WindowStartHour, out.Source[FieldWindowStartHour] = &v, SourceEnv
	}
	if env.WindowEndHour != nil {
		v := *env.WindowEndHour
		out.WindowEndHour, out.Source[FieldWindowEndHour] = &v, SourceEnv
	}

	row, ok, err := loadScheduleRow(ctx, sdb)
	if err != nil {
		return Schedule{}, err
	}
	if ok {
		out.Configured = true
		if row.enabled.Valid {
			out.Enabled, out.Source[FieldEnabled] = row.enabled.Int64 != 0, SourceDB
		}
		if row.mode.Valid && row.mode.String != "" {
			out.Mode, out.Source[FieldMode] = ScheduleMode(row.mode.String), SourceDB
		}
		if row.intervalHours.Valid {
			out.IntervalHours, out.Source[FieldIntervalHours] = int(row.intervalHours.Int64), SourceDB
		}
		if row.minFreePercent.Valid {
			out.MinFreePercent, out.Source[FieldMinFreePercent] = int(row.minFreePercent.Int64), SourceDB
		}
		if row.minFreeBytes.Valid {
			out.MinFreeBytes, out.Source[FieldMinFreeBytes] = row.minFreeBytes.Int64, SourceDB
		}
		// A configured row is authoritative for the window in both directions:
		// NULL here means the admin cleared it, which must beat an env default.
		if row.windowStartHour.Valid {
			v := int(row.windowStartHour.Int64)
			out.WindowStartHour = &v
		} else {
			out.WindowStartHour = nil
		}
		out.Source[FieldWindowStartHour] = SourceDB
		if row.windowEndHour.Valid {
			v := int(row.windowEndHour.Int64)
			out.WindowEndHour = &v
		} else {
			out.WindowEndHour = nil
		}
		out.Source[FieldWindowEndHour] = SourceDB
	}

	applyJournalOutcome(&out, journal)
	return out, nil
}

// applyJournalOutcome folds the last run's result in from the journal.
func applyJournalOutcome(s *Schedule, st State) {
	if st.Phase == PhaseIdle || st.StartedAt.IsZero() {
		return
	}
	at := st.StartedAt
	if st.FinishedAt != nil {
		at = *st.FinishedAt
	}
	s.LastRunAt = &at
	switch st.Phase {
	case PhaseDone:
		s.LastStatus = "ok"
		if st.FreedBytes > 0 {
			freed := st.FreedBytes
			s.LastFreedBytes = &freed
		}
	case PhaseFailed:
		s.LastStatus = "failed"
		s.LastError = st.Error
	case PhaseInterrupted:
		s.LastStatus = "skipped"
		s.LastError = st.Message
	default:
		// Still running: report no outcome rather than a stale one.
		s.LastRunAt = nil
	}
}

// ValidateSchedule rejects values that would produce a schedule that can
// never fire, or one that fires constantly.
func ValidateSchedule(s Schedule) error {
	if s.Mode != ModeIncremental && s.Mode != ModeFull {
		return fmt.Errorf("%w: mode must be %q or %q", ErrInvalidSchedule, ModeIncremental, ModeFull)
	}
	if s.IntervalHours < 1 {
		return fmt.Errorf("%w: interval_hours must be at least 1", ErrInvalidSchedule)
	}
	if s.MinFreePercent < 0 || s.MinFreePercent > 100 {
		return fmt.Errorf("%w: min_free_percent must be between 0 and 100", ErrInvalidSchedule)
	}
	if s.MinFreeBytes < 0 {
		return fmt.Errorf("%w: min_free_bytes must not be negative", ErrInvalidSchedule)
	}
	for name, h := range map[string]*int{
		FieldWindowStartHour: s.WindowStartHour,
		FieldWindowEndHour:   s.WindowEndHour,
	} {
		if h != nil && (*h < 0 || *h > 23) {
			return fmt.Errorf("%w: %s must be between 0 and 23", ErrInvalidSchedule, name)
		}
	}
	// Half a window can never be satisfied, and silently ignoring the other
	// half would be worse than refusing.
	if (s.WindowStartHour == nil) != (s.WindowEndHour == nil) {
		return fmt.Errorf("%w: set both window_start_hour and window_end_hour, or neither", ErrInvalidSchedule)
	}
	return nil
}

// SaveSchedule merges a patch onto the current effective schedule and writes
// the row. Returns the schedule as it now resolves.
func SaveSchedule(ctx context.Context, sdb *sql.DB, env ScheduleEnv, mode AutoVacuum, journal State,
	patch SchedulePatch, updatedBy string) (Schedule, error) {

	cur, err := ScheduleFor(ctx, sdb, env, mode, journal)
	if err != nil {
		return Schedule{}, err
	}
	next := cur
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	if patch.Mode != nil {
		next.Mode = ScheduleMode(*patch.Mode)
	}
	if patch.IntervalHours != nil {
		next.IntervalHours = *patch.IntervalHours
	}
	if patch.MinFreePercent != nil {
		next.MinFreePercent = *patch.MinFreePercent
	}
	if patch.MinFreeBytes != nil {
		next.MinFreeBytes = *patch.MinFreeBytes
	}
	if patch.WindowStartHour.Set {
		next.WindowStartHour = patch.WindowStartHour.Value
	}
	if patch.WindowEndHour.Set {
		next.WindowEndHour = patch.WindowEndHour.Value
	}
	if err := ValidateSchedule(next); err != nil {
		return Schedule{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = sdb.ExecContext(ctx, `
		INSERT INTO maintenance_settings
			(id, enabled, mode, interval_hours, min_free_percent, min_free_bytes,
			 window_start_hour, window_end_hour, updated_at, updated_by)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			mode = excluded.mode,
			interval_hours = excluded.interval_hours,
			min_free_percent = excluded.min_free_percent,
			min_free_bytes = excluded.min_free_bytes,
			window_start_hour = excluded.window_start_hour,
			window_end_hour = excluded.window_end_hour,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		boolToInt(next.Enabled), string(next.Mode), next.IntervalHours,
		next.MinFreePercent, next.MinFreeBytes,
		nullableInt(next.WindowStartHour), nullableInt(next.WindowEndHour),
		now, nullableString(updatedBy),
	)
	if err != nil {
		return Schedule{}, fmt.Errorf("upsert maintenance_settings: %w", err)
	}
	return ScheduleFor(ctx, sdb, env, mode, journal)
}

// DueNow reports whether an automatic run should start, and why not when it
// should not. Every condition has to hold at once; a tick that does not fire
// is silent and simply tries again next hour.
func DueNow(s Schedule, stats Stats, now time.Time, lastRun *time.Time, activeJobs int) (bool, string) {
	if !s.Enabled {
		return false, "automatic reclaim is disabled"
	}
	if lastRun != nil && now.Sub(*lastRun) < time.Duration(s.IntervalHours)*time.Hour {
		return false, "the interval since the last run has not elapsed"
	}
	if stats.ReclaimablePercent < float64(s.MinFreePercent) || stats.ReclaimableBytes < s.MinFreeBytes {
		return false, "the database has not wasted enough space to be worth reclaiming"
	}
	if s.WindowStartHour != nil && s.WindowEndHour != nil && !inWindow(now.Hour(), *s.WindowStartHour, *s.WindowEndHour) {
		return false, "outside the configured time window"
	}
	if activeJobs > 0 {
		return false, "an indexing or clone job is in flight"
	}
	if s.Mode == ModeFull {
		if stats.AutoVacuum == AutoVacuumNone && stats.ReclaimableBytes == 0 {
			return false, "there is nothing to reclaim"
		}
		if stats.FreeDiskBytes > 0 && stats.FreeDiskBytes < stats.RequiredDiskBytes {
			return false, "there is not enough free disk space for the copy"
		}
	}
	if s.Mode == ModeIncremental && stats.AutoVacuum != AutoVacuumIncremental {
		return false, "the database is not in incremental auto-vacuum mode"
	}
	return true, ""
}

// inWindow handles a window that wraps past midnight, which is the shape an
// operator actually wants (22:00–04:00).
func inWindow(hour, start, end int) bool {
	if start == end {
		return hour == start
	}
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
