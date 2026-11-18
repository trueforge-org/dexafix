# Estimates

## How it works

A smart scale is convenient but biased. A DEXA scan is accurate but rare. The
app uses the difference between the two (the offset) to correct the scale
between scans.

- Stored readings are never changed. Everything below is calculated from them
  and kept in a memory cache until the data or the date changes (see
  [Estimate cache](../developer/03-estimation-details.md#estimate-cache)).
- First the app builds its own per-day series: the most accurate reading of
  each day, averaged over a centred 7-day window of real readings, with the
  gaps filled in by straight lines.
- The offset is `DEXA - the actual reading` on the day of each DEXA scan. The
  reading is the scale's own value that day, not the smoothed series.
- On a day with a DEXA value, that value is used.
- On every other day, the series is corrected by the nearby offset.

Example: one DEXA scan on 2026-03-01 with body fat 18.0 %, while the scale says
20.0 % every day. The reading on the scan day is 20.0 %, so the offset is
-2.0 percentage points.

| Date       | Scale (%) | Estimate (%) | Origin                   |
| ---------- | --------- | ------------ | ------------------------ |
| 2026-03-01 | 20.0      | 18.0         | `dexa`                   |
| 2026-03-02 | 20.0      | 18.0         | `measured`               |
| 2026-03-05 | none      | 18.0         | `interpolated` or `held` |

Fat mass and fat-free mass are then calculated from the body fat % and the
weight on every day with a weight, whether or not a calibration applies. The
scale's own values are used only when they cannot be calculated.

The full rules are in [Estimation details](../developer/03-estimation-details.md).


## The daily estimates step by step {#how-estimates-work}

Each field is handled on its own. The estimated fields are weight, body fat, fat mass, fat-free mass, bone mass, lean mass, hydration, visceral fat index, BMI, FMI, LMI and the A/G ratio.

1. **Daily average.** Readings are averaged per day, per field and per group (method plus athlete mode). Missing values are ignored.
2. **Most accurate reading.** Each day uses the group whose method ranks highest for that field ([Method accuracy](#method-accuracy)). A method without a value for the field is skipped, so a caliper reading with only body fat does not hide the scale weight.
3. **Rolling average.** Each day is averaged over the real readings within 3 days before and after it (a 7-day window, shorter at the ends). Only real readings count, never filled-in days. With at least 3 days with a real reading in the window, that average is the day's value; otherwise a day with a reading keeps it.
4. **Missing days** get a value on a straight line between the nearest days that have one (origin `interpolated`). After the last such day the value is repeated up to today (origin `held`).
5. **Calibration.** On each DEXA day, the offset (DEXA minus the scale's reading that day) is recorded. See [Calibration](#calibration).
6. **Estimate.** A DEXA value is used as it is (origin `dexa`). Other days are corrected by the offset: before the first calibration with its offset, between two calibrations blended with a smooth curve, after the last calibration with the last offset.

Fat mass is calculated as weight x body fat % / 100 on every day with a weight, and fat-free mass as weight minus fat mass, so the two always add up to the weight. Lean mass is fat-free mass minus bone mass (only on days with both). BMI, FMI and LMI are weight, fat mass and lean mass divided by height squared; without a height in Settings they are left out. The A/G ratio is android fat % divided by gynoid fat %, from DEXA scans; after a scan the latest ratio is held until the next one.

Only body fat % and bone mass are corrected with an offset. Weight is never corrected, nor are hydration and visceral fat index.

## Reading the table {#estimate-badges}

- A **DEXA** badge marks a value from a DEXA scan; a **corr.** badge marks a corrected value.
- Faded values are interpolated or held. A gray **est** tag in front of a date marks a row with a value interpolated between two readings.
- Hover a value to see its origin (`measured`, `interpolated`, `held`, `dexa`), the method and mode it is based on, and the raw reading.
- The overview tiles show the latest value with the change; the chart has a selector for the measurement and the time range.

## Method accuracy {#method-accuracy}

- **What:** a ranking of the measurement methods, most accurate first. Page: Settings.
- **Default:** hydrostatic, BodPod, caliper, smart scale, tape, visual AI.
- **Effect:** when several methods have a reading for the same field on a day, the highest-ranked wins.
- **How to change:** drag the rows (or use the up and down buttons); without JavaScript there are rank number inputs. In the API: `PUT /api/v1/users/{id}/settings` with `"method_ranking": ["bodpod", "scale"]`; methods you leave out follow in default order.
- **Gotcha:** a method only wins for fields it actually measured.

## Athlete mode {#athlete-mode}

- **What:** many scales have an athlete setting that changes their estimate. A reading in athlete mode is a different kind of reading from a normal one.
- **Setting:** *Athlete mode by default* in Settings. It presets the checkbox for new readings, is applied to API readings that leave `athlete_mode` out, and to Withings imports. Default: off.
- **Effect:** each reading keeps its own flag. DEXA calibration is kept separately for normal and athlete readings. If the normal and athlete group of one method both have a value on a day, the one matching your default wins.
- **Gotcha:** changing the default does not change stored readings.

## Calibration {#calibration}

The offset is `DEXA - the reading on the scan day`, using the scale's own value that day, not the 7-day average. Every group (method and athlete mode) with a reading of that field on the scan day gets its own offset; several readings of one group on one day are averaged first. A group without a reading on the scan day gets no offset from that scan.

Example: a DEXA scan on 2026-03-01 says body fat 18.0 %, while the scale says 20.0 %. The offset is -2.0 percentage points, so on 2026-03-02 a scale reading of 20.0 % becomes an estimate of 18.0 %.

The *Calibrations in use* card on the estimates page lists each scan's offsets per method and mode. If it says none, add scale readings on the day of a DEXA scan.

## Range and columns {#estimate-range}

- **Range:** 30 days, 90 days (default), 1 year or all time. It applies to both the chart and the daily table. Query parameter `range` with the values `30`, `90`, `365` or `all`.
- **Columns:** see below.

### Columns {#estimate-columns}

The *Columns* menu above the table shows and hides columns. The choice is remembered per account. Columns added later start out visible.

## Per-day re-import and Strength Level {#resync-day}

With Withings connected, a **↻** button on each row re-imports that one day (see [Re-import one day](04-withings-and-strength-level.md#re-import-one-day)). With Strength Level enabled for you, a **⇪** button sends that day's weight and body fat to Strength Level (see [Strength Level](04-withings-and-strength-level.md#strengthlevel)).
