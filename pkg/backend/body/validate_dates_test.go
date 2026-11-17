package bodybackend

import (
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

// The body service checks the dates again: it does not trust the main backend to have done it.
func TestTimesThatCannotBeRealAreRefused(t *testing.T) {
	w := 80.0
	now := time.Now()
	for name, at := range map[string]time.Time{
		"zero":         {},
		"year 1":       time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		"before 1900":  time.Date(1899, 12, 31, 23, 0, 0, 0, time.UTC),
		"year 9999":    time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		"a week ahead": now.Add(7 * 24 * time.Hour),
	} {
		if validScaleReading(&models.ScaleReading{MeasuredAt: at, WeightKg: &w}) == nil {
			t.Errorf("a reading %s was accepted", name)
		}
		if validDexaScan(&models.DexaScan{MeasuredAt: at, WeightKg: &w}) == nil {
			t.Errorf("a scan %s was accepted", name)
		}
	}
	for name, at := range map[string]time.Time{"1900": earliestMeasurement, "now": now, "a zone ahead": now.Add(23 * time.Hour)} {
		if err := validScaleReading(&models.ScaleReading{MeasuredAt: at, WeightKg: &w}); err != nil {
			t.Errorf("a reading from %s was refused: %v", name, err)
		}
		if err := validDexaScan(&models.DexaScan{MeasuredAt: at, WeightKg: &w}); err != nil {
			t.Errorf("a scan from %s was refused: %v", name, err)
		}
	}
}
