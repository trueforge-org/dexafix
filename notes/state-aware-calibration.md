# State-aware DEXA calibration: where we stopped

Status: research and test harness only. The state-aware method itself is **not written yet**.
Branch: `feat/state-calibration` (commits `ea299df`, `a73ac35`; not pushed).
Written for: whoever picks this up once there is a second real DEXA scan.

## The question

Today a DEXA scan corrects the scale with one fixed amount: `offset = DEXA − scale reading on the scan day`,
for body fat % and bone mass (`pkg/estimate/estimate.go`, calibration code in `Compute`, `offsetAt`).
Between two scans the offset slides over **time** with a smoothstep. Before the first and after the last
scan the nearest offset is held.

Hypothesis: the scale's error is not constant. It follows the body's state, mainly **true lean mass**
(someone stays at 73 kg, gains lean mass and loses fat, and the scale keeps reading more fat). So the
offset should be a function of the body's state, not of the calendar. We also assume the error is smallest
near "normal" body fat % and lean mass.

Decisions made with the user:

- Use **lean mass / LMI** as the state variable, not FFMI (bone barely varies). Internally lean mass in kg,
  so height is not needed.
- From 2 scans: lean mass. From 3 scans: lean mass plus body fat %.
- **Bone mass keeps today's behaviour** (smoothstep between scans, nearest offset held outside them).
- On by default **if the backtest supports it**, with a toggle in the user's settings (new migration).
- Limited extrapolation outside the scanned range.

## What the backtest found (today's "time" method only)

`pkg/estimate/backtest_test.go` builds worlds where the true body fat is known and the scale's error
follows an assumed law of lean mass: `offset = off0 + slope·(lean − lean0)`, slope in pp of body fat per
kg of lean mass. Plausible slopes are 0 to −0.5 (about −1.4 would be a scale completely blind to lean mass).

Mean absolute error of body fat in pp, time method:

| World | slope 0 | slope −0.5 |
| --- | --- | --- |
| Generated recomposition, after scan 2 | 0.87 | 1.12 |
| Generated bulk-then-cut, leave one out | 0.66 | 0.97 |
| Generated, after scan 3 (noise of one calibration only) | 0.69 | 0.69 |
| Real readings + 3 fake scans (8.1 kg lean spread), after scan 2 | 0.74 | 1.44 |
| Same, after scan 3 | 0.41 | 2.28 |
| Same, leave one out | 0.82 | 1.72 |

In plain terms:

- With no lean-mass dependence the time method is off by about 0.7–0.9 pp. That is noise (scale about
  0.45 pp per day, DEXA about 0.7 pp) and no method can remove it.
- With a dependence it gets worse as lean mass moves away from the scan state: about 0.1–0.3 pp on a calm
  history, 0.4–1.5 pp on the trajectory built from the real readings.
- If the body does not change, nothing is lost.
- **All of this assumes the lean-mass dependence exists. The backtest cannot confirm that.** Only real scans
  at different lean masses can.

## Noise facts that set the design

- One calibration is off by about 0.83 pp (0.69 as a mean absolute error).
- A slope fitted from two scans is off by about `1.0 pp / Δlean` (DEXA noise alone gives √2·0.7).
  At a 3 kg lean spread that is 0.34 pp/kg, as large as the slope itself. So the **gate is 5 kg of lean
  spread** (and body fat spread ≥ 4–5 pp for a body-fat-only fit), with the slope shrunk by
  `spread² / (spread² + k²)`, k about 3–4 kg.
- The fixed-point iteration needs `|c·w/100 + b| < 1`; keep `|c| ≤ 1` (not 2).
- A history may not have room for scans 3 kg apart: the real export allowed fake scans only about 2 kg
  apart (4 scans over 8.1 kg).

## The method to build (step 1, not started)

Only for `fat_ratio_pct`, in a new `pkg/estimate/statecal.go` (pure, no I/O), behind
`Options.StateCalibration`:

1. State per scan: `lean` (scan lean mass, else fat-free − bone, else weight·(1−bf) − bone) and `bf`.
2. Fit per reading group: 2+ scans: `offset = a + c·(lean − mean)` if the lean spread passes the gate, else
   on `bf` if that spread passes. 3+ scans: both, unless |corr(lean, bf)| ≥ 0.9. Ridge least squares,
   slope shrinkage, sanity limits, otherwise fall back to the time blend.
3. Residual time blend: `offset(d) = model(state(d)) + smoothstep blend of the residuals`.
4. State on a day: fixed point `bf = scaleBf + offset(lean(bf))` with `lean(bf) = w·(1−bf/100) − bone`
   (at most 8 iterations). Bone is corrected first.
5. Extrapolation: clamp the state to `[min−ext, max+ext]`, `ext = min(span/3, 3 kg or 3 pp)`; cap the
   offset at 1.25× the largest scan offset.
6. The scale side of each calibration for the fit uses the 7-day mean around the scan day (the existing
   time offset keeps the single day).
7. Report the model used per group in `Result`, additive JSON.
8. A `time-7d` baseline in `Compute` (time blend anchored on the 7-day mean of the scan day), so a win is
   not just a better anchor.

Setting (step 2): migration `00007_state_calibration.sql` (sqlite `INTEGER NOT NULL DEFAULT 1`, postgres
`BOOLEAN NOT NULL DEFAULT TRUE`), then follow `AthleteDefault` end to end: `pkg/models`, `sqlstore/rows.go`,
`UpdateUserSettings`, `apiclient` and internal settings, public API (`*bool`, absent means keep),
export, `bodyapi.EstimateParams` and the cache hash in `pkg/backend/body/estimates.go`, the Settings page.
Docs (step 3): `docs/developer/03-estimation-details.md`, `docs/user/03-estimates.md`,
`docs/developer/02-json-api.md`, then `go generate ./pkg/frontend`.

## Decision rule for the default

Over at least 16 histories:

- (a) Null worlds (slope 0, flat, small spreads): state MAE ≤ time MAE + 0.03, and identical output when the
  gates are closed.
- (b) Wrong-model worlds (saturating law; still to add: law driven by body fat %, a lagging law, slow time
  drift): no worse than +0.10.
- (c) Slope −0.3 with a spread above the gate: better than both `time` and `time-7d` by at least 0.10 pp on
  leave-one-out and after scan 2.
- (d) Real readings + fake scans at slope 0: no worse.

If (c) fails: ship it off by default (setting stays) or drop it. A linear law is the very thing the
method's fixed point inverts, so the correct-model numbers are an upper bound.

## When the second real scan arrives

1. Put the new export at `dexafix-userdata.json` in the repository root (it is gitignored; never commit it).
2. Run the real tier with your time zone (it decides which day a reading belongs to):
   `go test -count=1 -run BacktestOnTheReal -v ./pkg/estimate -args -tz=Europe/Amsterdam`
   With 2 real scans the harness adds fewer fake ones. With 3 real scans it also prints the real forward
   check: each scan predicted from the earlier scans, compared with what DEXA said.
3. Look at the **real** lean mass and body fat of the two scans against the scale on those days:
   - the scale-to-DEXA offsets of the two scans, and the lean-mass difference between them. The slope is
     the offset difference divided by the lean difference. If the lean difference is under about 5 kg the
     slope is mostly noise (see above); if the offsets agree within about 1 pp, the time method is fine;
   - one slope from one pair of scans cannot prove anything. Three scans or more are needed to test a
     prediction on a scan the model has not seen.
4. Then build step 1 and run the decision rule. With real scans, add them to the rule as (e): the state
   method predicts each real scan from the earlier ones at least as well as the time method.

## Running what exists

```bash
go test -count=1 -run Backtest ./pkg/estimate            # about 7 s; the real tier skips without the export
go test -count=1 -run BacktestOnTheReal -v ./pkg/estimate -args -tz=UTC -export=/path/to/export.json
```

Harness notes (from an independent review):

- The scale error law acts on the synthetic **readings**, never on the truth, so nothing feeds back and the
  real scan stays consistent with the world. The truth is a 21-day smooth of the real scale values anchored
  on the real DEXA scan.
- Histories are averaged over 16 seeds because a single one is mostly noise. Float sums run over maps, so
  compare with a tolerance.
- The real tier passes the user's time zone and `Today` to `Compute`, as the body service does
  (`pkg/backend/body/estimates.go`). The ranking and athlete default are the defaults; untested with others.
- Only aggregate numbers are logged; do not commit anything derived per day from the export.
