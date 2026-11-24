package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestNowBlock(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Makassar")
	if err != nil {
		t.Fatal(err)
	}
	// 17:05 UTC is 01:05 the next day in Makassar (UTC+8).
	now := time.Date(2026, 11, 30, 17, 5, 0, 0, time.UTC)
	got := NowBlock(now, loc)
	for _, want := range []string{"Tuesday", "1 December 2026", "01:05", "Asia/Makassar", "UTC+8:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("NowBlock missing %q:\n%s", want, got)
		}
	}
}

func TestTimeZone(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	if got := TimeZone().String(); got != "Europe/Berlin" {
		t.Errorf("TimeZone() = %q, want Europe/Berlin", got)
	}

	t.Setenv("TZ", "Not/AZone")
	if got := TimeZone(); got != time.UTC {
		t.Errorf("invalid TZ should fall back to UTC, got %q", got)
	}
}
