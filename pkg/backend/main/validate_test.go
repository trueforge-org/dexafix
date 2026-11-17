package mainbackend

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

func TestAPIRangeChecks(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	if err := validScaleReading(&models.ScaleReading{MeasuredAt: time.Now(), WeightKg: f(80), FatRatioPct: f(20)}); err != nil {
		t.Errorf("valid reading rejected: %v", err)
	}
	for name, r := range map[string]models.ScaleReading{
		"nan":         {MeasuredAt: time.Now(), WeightKg: f(math.NaN())},
		"zero weight": {MeasuredAt: time.Now(), WeightKg: f(0)},
		"negative":    {MeasuredAt: time.Now(), WeightKg: f(-5)},
		"bf over 100": {MeasuredAt: time.Now(), FatRatioPct: f(101)},
		"long notes":  {MeasuredAt: time.Now(), Notes: strings.Repeat("x", 501)},
	} {
		if validScaleReading(&r) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if validDexaScan(&models.DexaScan{MeasuredAt: time.Now(), AndroidFatPct: f(-1)}) == nil {
		t.Error("negative android fat accepted")
	}
	if err := validDexaScan(&models.DexaScan{MeasuredAt: time.Now(), TScore: f(-2.5), BMDGCm2: f(1.2)}); err != nil {
		t.Errorf("valid scan rejected: %v", err)
	}
}
func TestCalendarDateUsesTheZone(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no tz data")
	}
	// the API sends the scan at local midnight: its date in Sydney is 1 March, not 28 February (UTC)
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, sydney)
	if got := calendarDate(at, sydney); !got.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("calendarDate = %v", got)
	}
}

// One reading in year 1 or 9999 makes the estimates allocate a series of every day in between.
func TestTimesThatCannotBeRealAreRefused(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	now := time.Now()
	for name, at := range map[string]time.Time{
		"zero":            {},
		"year 1":          time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		"before 1900":     time.Date(1899, 12, 31, 23, 0, 0, 0, time.UTC),
		"year 9999":       time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		"a week ahead":    now.Add(7 * 24 * time.Hour),
		"just over a day": now.Add(25 * time.Hour),
	} {
		if validScaleReading(&models.ScaleReading{MeasuredAt: at, WeightKg: f(80)}) == nil {
			t.Errorf("a reading %s was accepted", name)
		}
		if validDexaScan(&models.DexaScan{MeasuredAt: at, WeightKg: f(80)}) == nil {
			t.Errorf("a scan %s was accepted", name)
		}
	}
	for name, at := range map[string]time.Time{
		"1900":            earliestMeasurement,
		"today":           now,
		"in a zone ahead": now.Add(23 * time.Hour),
	} {
		if err := validScaleReading(&models.ScaleReading{MeasuredAt: at, WeightKg: f(80)}); err != nil {
			t.Errorf("a reading from %s was refused: %v", name, err)
		}
	}
}
