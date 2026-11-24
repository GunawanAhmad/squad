package assistant

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const DefaultTZ = "Asia/Makassar"

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
