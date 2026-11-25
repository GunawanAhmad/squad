package assistant

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"squad/store"
)

const DefaultTZ = "Asia/Makassar"

// Day and clock handling lives here and nowhere else: settings.tz decides the
// zone, settings.day_starts_at decides where a day begins (04:00 by default),
// so chatting at 00:30 stays in the previous day's thread.
const (
	DefaultDayStartsAt = 4 * time.Hour
	DefaultMorningAt   = 8 * time.Hour
	DefaultEveningAt   = 21 * time.Hour
)

const (
	SettingTZ          = "tz"
	SettingDayStartsAt = "day_starts_at"
	SettingMorningAt   = "morning_at"
	SettingEveningAt   = "evening_at"
	SettingJobsModel   = "jobs_model"
)

// Settings is the time-related configuration the daily loop needs.
type Settings struct {
	TZ          string
	Loc         *time.Location
	DayStartsAt time.Duration
	MorningAt   time.Duration
	EveningAt   time.Duration
	JobsModel   string
}

// SettingsStore is the slice of the store LoadSettings reads.
type SettingsStore interface {
	Settings(ctx context.Context) (map[string]string, error)
}

// DefaultSettings comes from the environment, so a fresh database already
// behaves like Phase 0 did.
func DefaultSettings() Settings {
	tz := os.Getenv("TZ")
	if tz == "" {
		tz = DefaultTZ
	}
	return Settings{
		TZ: tz, Loc: TimeZone(), DayStartsAt: DefaultDayStartsAt,
		MorningAt: DefaultMorningAt, EveningAt: DefaultEveningAt,
	}
}

// LoadSettings reads settings and falls back to the defaults for anything
// missing or unparseable. It never fails: a bad value is logged and ignored.
func LoadSettings(ctx context.Context, s SettingsStore) Settings {
	out := DefaultSettings()
	if s == nil {
		return out
	}
	vals, err := s.Settings(ctx)
	if err != nil {
		log.Printf("assistant: reading settings: %v", err)
		return out
	}
	if v := strings.TrimSpace(vals[SettingTZ]); v != "" && v != out.TZ {
		loc, err := time.LoadLocation(v)
		if err != nil {
			log.Printf("assistant: settings tz %q is not known (%v); keeping %s", v, err, out.TZ)
		} else {
			out.TZ, out.Loc = v, loc
		}
	}
	if d, err := parseClock(vals[SettingDayStartsAt], out.DayStartsAt); err != nil {
		log.Printf("assistant: day_starts_at: %v", err)
	} else {
		out.DayStartsAt = d
	}
	if d, err := parseClock(vals[SettingMorningAt], out.MorningAt); err != nil {
		log.Printf("assistant: morning_at: %v", err)
	} else {
		out.MorningAt = d
	}
	if d, err := parseClock(vals[SettingEveningAt], out.EveningAt); err != nil {
		log.Printf("assistant: evening_at: %v", err)
	} else {
		out.EveningAt = d
	}
	out.JobsModel = strings.TrimSpace(vals[SettingJobsModel])
	return out
}

// ParseClock reads "HH:MM" as a duration since local midnight.
func ParseClock(v string) (time.Duration, error) { return parseClock(v, -1) }

func parseClock(v string, def time.Duration) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if def < 0 {
			return 0, fmt.Errorf("empty time")
		}
		return def, nil
	}
	t, err := time.Parse("15:04", v)
	if err != nil {
		if def >= 0 {
			return def, fmt.Errorf("%q is not HH:MM", v)
		}
		return 0, fmt.Errorf("%q is not HH:MM", v)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// ClockString renders a duration since midnight as "HH:MM" (settings JSON).
func ClockString(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour)%24, int(d/time.Minute)%60)
}

// LogicalDay is the calendar day a moment belongs to (date at midnight UTC).
func LogicalDay(now time.Time, loc *time.Location, startsAt time.Duration) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return store.Day(now.In(loc).Add(-startsAt))
}

// At is a wall-clock time on a calendar day, in the user's zone.
func At(day time.Time, clock time.Duration, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(day.Year(), day.Month(), day.Day(), int(clock/time.Hour)%24, int(clock/time.Minute)%60, 0, 0, loc)
}

// Now is the wall clock the model is told about.
func Now() time.Time { return time.Now().In(TimeZone()) }

// TimeZone loads TZ (default Asia/Makassar), falling back to UTC with a log
// line rather than silently using the server's zone.
func TimeZone() *time.Location {
	name := os.Getenv("TZ")
	if name == "" {
		name = DefaultTZ
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("TZ %q is not a known time zone (%v); using UTC", name, err)
		return time.UTC
	}
	return loc
}

// NowBlock is what makes "what day is it?" answerable. It is part of the
// system prompt on both routes.
func NowBlock(now time.Time, loc *time.Location) string {
	t := now.In(loc)
	_, offset := t.Zone()

	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hh, mm := offset/3600, (offset%3600)/60

	var b strings.Builder
	b.WriteString("\n<now>\n")
	fmt.Fprintf(&b, "Current local date and time: %s, %s, %s\n", t.Format("Monday"), t.Format("2 January 2006"), t.Format("15:04"))
	fmt.Fprintf(&b, "Time zone: %s (UTC%s%d:%02d)\n", t.Location(), sign, hh, mm)
	b.WriteString("If asked for the date or time, answer from this.\n")
	b.WriteString("</now>\n")
	return b.String()
}
