package estimate

// Backtest of the calibration methods against a known truth.
//
// A DEXA scan is the only ground truth there is, so the methods are compared on worlds where the
// truth is known by construction: the scale's body fat % error is made to follow an assumed law of
// the body's lean mass (offset = DEXA − scale = off0 + slope·(lean − lean0)), and each method is
// asked for the true body fat % on days it was not told about.
//
// There are three tiers of data:
//
//   - fake: whole histories generated here from a fixed seed (runs in CI);
//   - real: the readings and scans of an export file (skipped when the file is missing);
//   - real + fake scans: the real readings, with fake DEXA scans added until there are enough of
//     them to fit and to test a method (skipped when the file is missing).
//
// The export is read from -export, default dexafix-userdata.json in the repository root (ignored by
// git). Only aggregate numbers are logged; nothing derived from the file is committed.

import (
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

var exportTZ = flag.String("tz", "UTC", "time zone of the export's user for the real-data backtest, as in their settings: it decides which day a reading belongs to")

var exportPath = flag.String("export", "", "DexaFix body-data export for the real-data backtest (default: dexafix-userdata.json in the repository root)")

const (
	btScaleNoiseBF = 0.45 // pp; the day-to-day noise measured on a real export
	btScaleNoiseKg = 0.4  // kg
	btDexaNoiseBF  = 0.7  // pp; the lean mass of a scan follows from it, so the two errors are anti-correlated
	btBoneKg       = 2.7
	btOffsetBF     = 2.7 // pp: the scale reads lower than DEXA, as on the real export
	btMinGapDays   = 56  // a fake scan keeps this far from every other scan
	btMinLeanGapKg = 2.0 // ... and this far in lean mass (a short history may not have room for more)
	btSeeds        = 16  // fake histories averaged per cell: one history is mostly noise
)

// method is a way of calibrating, compared with the others on the same world.
type method struct {
	name string
	opts Options
}

// methods lists what the backtest compares. The state-aware method is added when it exists.
var methods = []method{{name: "time"}}

// errorLaw is the assumed scale error: DEXA − scale body fat %, as a function of the true lean mass.
// It is linear, or saturating (sat > 0: the same slope near lean0, levelling off at ±slope·sat kg),
// which a method that assumes a line does not know about.
type errorLaw struct{ off0, lean0, slope, sat float64 }

func (l errorLaw) at(lean float64) float64 {
	if l.sat > 0 {
		return l.off0 + l.slope*l.sat*math.Tanh((lean-l.lean0)/l.sat)
	}
	return l.off0 + l.slope*(lean-l.lean0)
}

// world is a history with a known truth.
type world struct {
	truth    map[time.Time]float64 // true body fat % per day
	readings []models.ScaleReading
	scans    []models.DexaScan // sorted by date
}

// fakeScan is a scan of a body with the true body fat %. Its only error is in the body fat %; fat,
// fat-free and lean mass follow from it at the scan's weight, as the errors of a real scan do.
func fakeScan(id int64, date time.Time, weight, trueBF, bone float64, rng *rand.Rand) models.DexaScan {
	bf := trueBF + rng.NormFloat64()*btDexaNoiseBF
	fat := weight * bf / 100
	return models.DexaScan{
		ID: id, MeasuredAt: date, WeightKg: f(weight), FatRatioPct: f(bf), FatMassKg: f(fat),
		FatFreeMassKg: f(weight - fat), LeanMassKg: f(weight - fat - bone), BoneMassKg: f(bone),
	}
}

// ramp is a piecewise linear series of n days through the (day, value) points, flat outside them.
func ramp(n int, pts ...[2]float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		x := float64(i)
		switch {
		case x <= pts[0][0]:
			out[i] = pts[0][1]
		case x >= pts[len(pts)-1][0]:
			out[i] = pts[len(pts)-1][1]
		default:
			for j := 1; j < len(pts); j++ {
				if x <= pts[j][0] {
					a, b := pts[j-1], pts[j]
					out[i] = a[1] + (b[1]-a[1])*(x-a[0])/(b[0]-a[0])
					break
				}
			}
		}
	}
	return out
}

// path is the true weight and lean mass (without bone) per day.
type path struct{ weight, lean []float64 }

const btDays = 1100

type btScenario struct {
	name     string
	p        path
	scanDays []int
}

var btScenarios = []btScenario{
	// The weight stays at 73 kg while 3.5 kg of fat turn into lean mass.
	{"recomposition", path{ramp(btDays, [2]float64{0, 73}), ramp(btDays, [2]float64{0, 54}, [2]float64{540, 57.5})}, []int{30, 330, 630, 1030}},
	// A bulk, then a cut: the lean mass goes up and comes back down.
	{"bulk-then-cut", path{ramp(btDays, [2]float64{0, 65}, [2]float64{360, 75}, [2]float64{600, 66}), ramp(btDays, [2]float64{0, 54}, [2]float64{360, 58}, [2]float64{600, 57})}, []int{30, 330, 630, 1030}},
	// Nothing changes: any slope a method finds here is noise.
	{"flat", path{ramp(btDays, [2]float64{0, 70}), ramp(btDays, [2]float64{0, 55})}, []int{30, 330, 630, 1030}},
}

// btSlopes are in pp of body fat per kg of lean mass. A scale that is blind to lean mass has a slope of
// about −100/weight ≈ −1.4; real foot-to-foot scales see part of it, so a plausible range is 0 to −0.5.
var btSlopes = []float64{0, -0.15, -0.3, -0.5}

func fakeWorld(p path, law errorLaw, scanDays []int, seed uint64) world {
	rng := rand.New(rand.NewPCG(seed, 7))
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	forced := map[int]bool{}
	for _, d := range scanDays {
		forced[d] = true // a scan day needs a reading, or the scan calibrates nothing
	}
	w := world{truth: map[time.Time]float64{}}
	for i := range p.weight {
		date := start.AddDate(0, 0, i)
		bf := (p.weight[i] - btBoneKg - p.lean[i]) / p.weight[i] * 100
		w.truth[date] = bf
		if !forced[i] && rng.Float64() < 0.15 {
			continue
		}
		sbf := bf - law.at(p.lean[i]) + rng.NormFloat64()*btScaleNoiseBF
		sw := p.weight[i] + rng.NormFloat64()*btScaleNoiseKg
		w.readings = append(w.readings, models.ScaleReading{MeasuredAt: date.Add(8 * time.Hour), WeightKg: &sw, FatRatioPct: &sbf, Method: models.MethodScale})
	}
	for k, d := range scanDays {
		date := start.AddDate(0, 0, d)
		w.scans = append(w.scans, fakeScan(int64(k+1), date, p.weight[d], w.truth[date], btBoneKg, rng))
	}
	return w
}

func estimates(w world, m method, scans []models.DexaScan) map[time.Time]float64 {
	r := Compute(scans, w.readings, m.opts)
	out := make(map[time.Time]float64, len(r.Days))
	for _, e := range r.Days {
		if v, ok := e.Values["fat_ratio_pct"]; ok {
			out[e.Date] = v.V
		}
	}
	return out
}

func scanDays(scans []models.DexaScan) map[time.Time]bool {
	out := map[time.Time]bool{}
	for _, s := range scans {
		out[day(s.MeasuredAt)] = true
	}
	return out
}

// forwardMAE is the mean absolute error of the body fat % against the truth on the days after the
// k-th scan (scan days left out), when only the first k scans are known: the "today" case.
func forwardMAE(w world, m method, k int) float64 {
	if k > len(w.scans) {
		return math.NaN()
	}
	est := estimates(w, m, w.scans[:k])
	last := day(w.scans[k-1].MeasuredAt)
	skip := scanDays(w.scans)
	sum, n := 0.0, 0
	for d, tr := range w.truth {
		v, ok := est[d]
		if !d.After(last) || skip[d] || !ok {
			continue
		}
		sum += math.Abs(v - tr)
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// leaveOneOutMAE hides each scan in turn and measures the error of the estimate on its day.
func leaveOneOutMAE(w world, m method) float64 {
	sum, n := 0.0, 0
	for i, s := range w.scans {
		rest := append(append([]models.DexaScan(nil), w.scans[:i]...), w.scans[i+1:]...)
		est := estimates(w, m, rest)
		d := day(s.MeasuredAt)
		if v, ok := est[d]; ok {
			if tr, ok := w.truth[d]; ok {
				sum += math.Abs(v - tr)
				n++
			}
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// scanForwardMAE predicts each scan from the scans before it and compares with what the scan said
// (the only measurement that exists for real data): the error includes the scan's own noise.
func scanForwardMAE(scans []models.DexaScan, readings []models.ScaleReading, m method) float64 {
	sum, n := 0.0, 0
	for i := 2; i < len(scans); i++ {
		if scans[i].FatRatioPct == nil {
			continue
		}
		r := Compute(scans[:i], readings, m.opts)
		for _, e := range r.Days {
			if e.Date.Equal(day(scans[i].MeasuredAt)) {
				if v, ok := e.Values["fat_ratio_pct"]; ok {
					sum += math.Abs(v.V - *scans[i].FatRatioPct)
					n++
				}
			}
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

type btResult struct{ fwd2, fwd3, loo float64 }

func runBacktest(w world, m method) btResult {
	return btResult{forwardMAE(w, m, 2), forwardMAE(w, m, 3), leaveOneOutMAE(w, m)}
}

// meanResult is the result of a method on btSeeds histories of a scenario, averaged.
func meanResult(sc btScenario, law errorLaw, m method) btResult {
	var sum btResult
	for seed := 1; seed <= btSeeds; seed++ {
		r := runBacktest(fakeWorld(sc.p, law, sc.scanDays, uint64(seed)), m)
		sum.fwd2 += r.fwd2
		sum.fwd3 += r.fwd3
		sum.loo += r.loo
	}
	n := float64(btSeeds)
	return btResult{sum.fwd2 / n, sum.fwd3 / n, sum.loo / n}
}

func TestBacktestTimeCalibrationLosesAccuracyWhenTheErrorFollowsLeanMass(t *testing.T) {
	results := map[string]map[float64]btResult{}
	for _, sc := range btScenarios {
		results[sc.name] = map[float64]btResult{}
		for _, slope := range btSlopes {
			law := errorLaw{off0: btOffsetBF, lean0: sc.p.lean[0], slope: slope}
			for _, m := range methods {
				r := meanResult(sc, law, m)
				if m.name == "time" {
					results[sc.name][slope] = r
				}
				t.Logf("%-14s slope %+.2f  %-6s  MAE pp over %d histories: after scan 2 %.2f, after scan 3 %.2f, leave one out %.2f", sc.name, slope, m.name, btSeeds, r.fwd2, r.fwd3, r.loo)
			}
		}
	}
	// The generator must produce the effect a state-aware method is meant to remove, and no more than
	// that. Thresholds are about half of what 16 histories showed, so they are not flaky.
	for name, byslope := range results {
		for slope, r := range byslope {
			// After scan 3 the lean mass no longer moves: what is left is the noise of one calibration.
			if r.fwd3 < 0.5 || r.fwd3 > 0.85 {
				t.Errorf("%s, slope %+.2f: the error after scan 3 is %.2f pp, the noise of one calibration should give about 0.69", name, slope, r.fwd3)
			}
		}
	}
	if d := results["recomposition"][-0.5].fwd2 - results["recomposition"][0].fwd2; d < 0.12 {
		t.Errorf("recomposition: a slope of -0.5 should cost the time method more than %.2f pp after scan 2, it costs %.2f", 0.12, d)
	}
	if d := results["bulk-then-cut"][-0.5].loo - results["bulk-then-cut"][0].loo; d < 0.15 {
		t.Errorf("bulk-then-cut: a slope of -0.5 should cost the time method more than %.2f pp when a scan is left out, it costs %.2f", 0.15, d)
	}
	for _, name := range []string{"recomposition", "bulk-then-cut"} {
		for i := 1; i < len(btSlopes); i++ {
			a, b := results[name][btSlopes[i-1]], results[name][btSlopes[i]]
			if b.loo < a.loo-0.02 {
				t.Errorf("%s: the leave-one-out error should grow with the slope: %.2f at %+.2f, %.2f at %+.2f", name, a.loo, btSlopes[i-1], b.loo, btSlopes[i])
			}
		}
	}
	for _, slope := range btSlopes {
		// Sums run over a map, so equal results may differ in the last bits.
		if a, b := results["flat"][0], results["flat"][slope]; math.Abs(a.fwd2-b.fwd2) > 1e-9 || math.Abs(a.fwd3-b.fwd3) > 1e-9 || math.Abs(a.loo-b.loo) > 1e-9 {
			t.Errorf("flat: the slope should not matter when nothing changes: %+v against %+v", a, b)
		}
	}
}

func TestBacktestASaturatingLawIsAWorldTheStateMethodDoesNotAssume(t *testing.T) {
	sc := btScenarios[1]
	for _, slope := range []float64{-0.3, -0.5} {
		law := errorLaw{off0: btOffsetBF, lean0: sc.p.lean[0], slope: slope, sat: 3}
		for _, m := range methods {
			r := meanResult(sc, law, m)
			t.Logf("%-14s saturating slope %+.2f  %-6s  MAE pp over %d histories: after scan 2 %.2f, after scan 3 %.2f, leave one out %.2f", sc.name, slope, m.name, btSeeds, r.fwd2, r.fwd3, r.loo)
			if math.IsNaN(r.fwd2) || math.IsNaN(r.loo) {
				t.Errorf("%s: no result: %+v", m.name, r)
			}
		}
	}
}

func TestBacktestNothingChangesInAFlatBodyWhateverTheSlopeIs(t *testing.T) {
	sc := btScenarios[2]
	var base map[time.Time]float64
	for _, slope := range btSlopes {
		w := fakeWorld(sc.p, errorLaw{off0: btOffsetBF, lean0: sc.p.lean[0], slope: slope}, sc.scanDays, 1)
		for _, m := range methods {
			est := estimates(w, m, w.scans)
			if slope == btSlopes[0] {
				base = est
				continue
			}
			for d, v := range est {
				if math.Abs(v-base[d]) > 1e-9 {
					t.Fatalf("%s, slope %+.2f: %v differs: %.6f against %.6f", m.name, slope, d, v, base[d])
				}
			}
		}
	}
}

// ---- real data ----

func loadExport(t *testing.T) ([]models.DexaScan, []models.ScaleReading) {
	t.Helper()
	path := *exportPath
	if path == "" {
		path = "../../dexafix-userdata.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no export to backtest on (%v): pass -export or put dexafix-userdata.json in the repository root", err)
	}
	var in struct {
		DexaScans     []models.DexaScan     `json:"dexa_scans"`
		ScaleReadings []models.ScaleReading `json:"scale_readings"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("the export does not parse: %v", err)
	}
	sort.SliceStable(in.DexaScans, func(i, j int) bool { return in.DexaScans[i].MeasuredAt.Before(in.DexaScans[j].MeasuredAt) })
	return in.DexaScans, in.ScaleReadings
}

// leanOf is the lean mass (without bone) a scan states.
func leanOf(s models.DexaScan) (float64, bool) {
	switch {
	case s.LeanMassKg != nil:
		return *s.LeanMassKg, true
	case s.FatFreeMassKg != nil && s.BoneMassKg != nil:
		return *s.FatFreeMassKg - *s.BoneMassKg, true
	}
	return 0, false
}

// dayNoiseBF is the standard deviation of one day's body fat % around its neighbour, from the
// differences of consecutive days (which carry the noise of two days).
func dayNoiseBF(readings []models.ScaleReading, loc *time.Location) float64 {
	sum, cnt := map[time.Time]float64{}, map[time.Time]int{}
	for _, r := range readings {
		if r.FatRatioPct != nil {
			sum[localDay(r.MeasuredAt, loc)] += *r.FatRatioPct
			cnt[localDay(r.MeasuredAt, loc)]++
		}
	}
	var diffs []float64
	for d, s := range sum {
		if s2, ok := sum[d.AddDate(0, 0, 1)]; ok {
			diffs = append(diffs, s2/float64(cnt[d.AddDate(0, 0, 1)])-s/float64(cnt[d]))
		}
	}
	if len(diffs) < 2 {
		return math.NaN()
	}
	ss := 0.0
	for _, x := range diffs {
		ss += x * x
	}
	return math.Sqrt(ss/float64(len(diffs))) / math.Sqrt2
}

// realWorld takes the real readings as the scale's readings and the first real scan as the anchor.
//
//   - The true body fat % of a day is the slowly smoothed (21 days) scale value plus the offset the
//     first scan shows, so the truth is DEXA on the scan day and follows the scale's own trend (its
//     weekly noise is not part of the truth).
//   - The true lean mass follows from it and the smoothed weight.
//   - The assumed error law is then applied to the scale: every real reading is moved by the law's
//     deviation from the anchor offset at that day's true lean mass, so the readings keep their real
//     noise, and a slope of 0 leaves them as they are. Nothing feeds back into the truth.
//   - Fake scans are added, with the error of a scan, on days whose lean mass is at least btMinLeanGapKg away
//     from that of every other scan (the farthest first), until there are minScans.
func realWorld(scans []models.DexaScan, readings []models.ScaleReading, loc *time.Location, slope float64, minScans int, seed uint64) (world, float64, bool) {
	if len(scans) == 0 || scans[0].FatRatioPct == nil {
		return world{}, 0, false
	}
	daily := map[time.Time]*struct {
		bf, w   float64
		nbf, nw int
	}{}
	for _, r := range readings {
		a := daily[localDay(r.MeasuredAt, loc)]
		if a == nil {
			a = &struct {
				bf, w   float64
				nbf, nw int
			}{}
			daily[localDay(r.MeasuredAt, loc)] = a
		}
		if r.FatRatioPct != nil {
			a.bf += *r.FatRatioPct
			a.nbf++
		}
		if r.WeightKg != nil {
			a.w += *r.WeightKg
			a.nw++
		}
	}
	// slow is the mean of the daily means in the 21 days around d.
	slow := func(d time.Time) (bf, w float64, ok bool) {
		var sb, sw float64
		nb, nw := 0, 0
		for k := -10; k <= 10; k++ {
			if a := daily[d.AddDate(0, 0, k)]; a != nil {
				if a.nbf > 0 {
					sb += a.bf / float64(a.nbf)
					nb++
				}
				if a.nw > 0 {
					sw += a.w / float64(a.nw)
					nw++
				}
			}
		}
		if nb < 5 || nw < 5 {
			return 0, 0, false
		}
		return sb / float64(nb), sw / float64(nw), true
	}
	d0 := day(scans[0].MeasuredAt)
	slowBF0, _, ok := slow(d0)
	if !ok {
		return world{}, 0, false
	}
	off0 := *scans[0].FatRatioPct - slowBF0
	bone := btBoneKg
	if scans[0].BoneMassKg != nil {
		bone = *scans[0].BoneMassKg
	}

	w := world{truth: map[time.Time]float64{}}
	lean, slowW := map[time.Time]float64{}, map[time.Time]float64{}
	var days []time.Time
	for d := range daily {
		sb, sw, ok := slow(d)
		if !ok {
			continue
		}
		w.truth[d] = sb + off0
		slowW[d] = sw
		lean[d] = sw*(1-w.truth[d]/100) - bone
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	if len(days) < 120 || lean[d0] == 0 {
		return world{}, 0, false
	}
	law := errorLaw{off0: off0, lean0: lean[d0], slope: slope}

	w.readings = make([]models.ScaleReading, len(readings))
	for i, r := range readings {
		if l, ok := lean[localDay(r.MeasuredAt, loc)]; ok && r.FatRatioPct != nil {
			moved := *r.FatRatioPct - (law.at(l) - off0)
			r.FatRatioPct = &moved
		}
		w.readings[i] = r
	}
	w.scans = append([]models.DexaScan(nil), scans...)

	hasReading := map[time.Time]bool{}
	for _, r := range readings {
		if r.FatRatioPct != nil && r.WeightKg != nil {
			hasReading[localDay(r.MeasuredAt, loc)] = true
		}
	}
	leanOfScan := func(s models.DexaScan) float64 {
		if l, ok := lean[day(s.MeasuredAt)]; ok {
			return l
		}
		return law.lean0
	}
	first, last := days[0].AddDate(0, 0, 14), days[len(days)-1].AddDate(0, 0, -14)
	rng := rand.New(rand.NewPCG(seed, 11))
	for id := int64(1000); len(w.scans) < minScans; id++ {
		var best time.Time
		bestScore := btMinLeanGapKg // kg of lean mass from the nearest scan, at least
		for _, d := range days {
			if d.Before(first) || d.After(last) || !hasReading[d] {
				continue
			}
			score, tooClose := math.Inf(1), false
			for _, s := range w.scans {
				if math.Abs(d.Sub(day(s.MeasuredAt)).Hours()/24) < btMinGapDays {
					tooClose = true
					break
				}
				score = min(score, math.Abs(lean[d]-leanOfScan(s)))
			}
			if !tooClose && score >= bestScore {
				best, bestScore = d, score
			}
		}
		if best.IsZero() {
			return world{}, 0, false
		}
		w.scans = append(w.scans, fakeScan(id, best, slowW[best], w.truth[best], bone, rng))
		sort.SliceStable(w.scans, func(i, j int) bool { return w.scans[i].MeasuredAt.Before(w.scans[j].MeasuredAt) })
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range w.scans {
		l := leanOfScan(s)
		lo, hi = min(lo, l), max(hi, l)
	}
	return w, hi - lo, true
}

func TestBacktestOnTheRealExportWithFakeScansAddedWhenThereAreTooFew(t *testing.T) {
	scans, readings := loadExport(t)
	loc, err := time.LoadLocation(*exportTZ)
	if err != nil {
		t.Fatalf("time zone %q: %v", *exportTZ, err)
	}
	// As production calls Compute: the user's time zone, and the last value held up to the last reading's day.
	var lastReading time.Time
	for _, r := range readings {
		lastReading = latest(lastReading, r.MeasuredAt)
	}
	t.Logf("export: %d scans, %d readings, time zone %s; scale noise on one day ≈ %.2f pp of body fat", len(scans), len(readings), loc, dayNoiseBF(readings, loc))
	for _, slope := range btSlopes {
		// As many scans as the history has room for: 4 if it can, else 3, else 2.
		var w world
		var spread float64
		ok := false
		for _, n := range []int{4, 3, 2} {
			if w, spread, ok = realWorld(scans, readings, loc, slope, max(n, len(scans)), 3); ok {
				break
			}
		}
		if !ok {
			t.Skip("the export cannot anchor the error law (no scan with a body fat % and enough readings around it) or has no room for the fake scans")
		}
		// The world agrees with the one real measurement there is.
		if got, want := w.truth[day(scans[0].MeasuredAt)], *scans[0].FatRatioPct; math.Abs(got-want) > 1e-9 {
			t.Errorf("slope %+.2f: the truth on the real scan's day is %.4f, the scan says %.4f", slope, got, want)
		}
		for _, m := range methods {
			m.opts.Location, m.opts.Today = loc, lastReading
			r := runBacktest(w, m)
			t.Logf("real readings, %d real + %d fake scans (lean mass spread %.1f kg), slope %+.2f  %-6s  MAE pp: after scan 2 %.2f, after scan 3 %.2f, leave one out %.2f",
				len(scans), len(w.scans)-len(scans), spread, slope, m.name, r.fwd2, r.fwd3, r.loo)
		}
	}
	if len(scans) >= 3 {
		for _, m := range methods {
			m.opts.Location, m.opts.Today = loc, lastReading
			t.Logf("real scans only, %-6s  MAE of each scan predicted from the earlier ones: %.2f pp", m.name, scanForwardMAE(scans, readings, m))
		}
	} else {
		t.Logf("fewer than 3 real scans: the real forward-chaining check needs a second and a third scan")
	}
}

func TestBacktestScanForwardErrorCanBeMeasuredOnAFakeHistory(t *testing.T) {
	sc := btScenarios[0]
	w := fakeWorld(sc.p, errorLaw{off0: btOffsetBF, lean0: sc.p.lean[0], slope: -0.3}, sc.scanDays, 1)
	got := scanForwardMAE(w.scans, w.readings, method{name: "time"})
	if math.IsNaN(got) || got <= 0 || got > 10 {
		t.Errorf("error of the scans predicted from earlier scans: %v", got)
	}
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
