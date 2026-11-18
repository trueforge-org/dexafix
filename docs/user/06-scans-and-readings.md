# DEXA scans and scale readings

## DEXA scans {#dexa-page}

Page: `/u/{id}/dexa`. A DEXA scan is your reference. A scan needs a date and at least one measurement. Numbers may use a decimal comma. Every field is optional.

| Field | JSON key | Unit | Meaning |
| --- | --- | --- | --- |
| Date | `measured_at` | date | Required, from 1900-01-01 to tomorrow. In the API an RFC 3339 timestamp; its date in your time zone is used. Scans are matched to scale readings by the reading's day in your time zone. |
| Weight | `weight_kg` | kg | Total body weight. Used as the estimate on the scan day but never corrected. |
| Body fat | `fat_ratio_pct` | % | Body fat percentage. Calibrates the scale's body fat %. If left out but fat mass and weight are known, it is calculated as fat mass / weight x 100. |
| Fat mass | `fat_mass_kg` | kg | Total fat mass. |
| Fat-free mass | `fat_free_mass_kg` | kg | Weight minus fat mass (includes bone). |
| Lean mass | `lean_mass_kg` | kg | Fat-free mass minus bone mass (lean soft tissue). |
| Bone mass (BMC) | `bone_mass_kg` | kg | Bone mineral content. Calibrates the scale's bone mass. |
| Visceral fat (VAT) | `visceral_fat_mass_kg` | kg | Visceral adipose tissue mass. Stored and exported; it is not used in estimates, because scale visceral fat is an index, not a mass. |
| BMD | `bmd_g_cm2` | g/cm² | Bone mineral density. |
| T-score | `t_score` | standard deviations | Bone density compared with a healthy young adult. |
| Z-score | `z_score` | standard deviations | Bone density compared with people of your age and sex. |
| Android fat | `android_fat_pct` | % | Fat percentage of the android (abdominal) region. |
| Gynoid fat | `gynoid_fat_pct` | % | Fat percentage of the gynoid (hip) region. With android fat it gives the A/G ratio. |
| Notes | `notes` | text | Up to 500 characters. |

A scan only calibrates the scale when the scale has a reading of the same kind on the same day. Add your scale readings for the scan day (see [Calibration](03-estimates.md#calibration)). Deleting a scan removes its calibration.

### Gray values (calculated while typing) {#auto-fill}

While you fill in a form, an empty field shows a gray value when it can be calculated from what you typed. Weight, body fat %, fat mass and fat-free mass calculate from each other: any two give the rest. On the DEXA form, lean mass + bone mass gives fat-free mass. A typed value is never replaced. Gray values are saved with the form unless you type something else. This needs JavaScript; without it the fields stay empty.

## Scale readings {#scale-page}

Page: `/u/{id}/scale`. A reading is one measurement from a scale or another method. The times are in your [time zone](01-using-the-app.md#time-zone). The page shows 100 readings per page, newest first, with **Older** and **Newer** links. The links use the query parameter `before`, a cursor that marks where the next page starts.

| Field | JSON key | Unit | Meaning |
| --- | --- | --- | --- |
| Date & time | `measured_at` | your time zone | Required, from 1900-01-01 to tomorrow. Several readings on one day (in your time zone) are averaged per method. The API takes an RFC 3339 timestamp. |
| Method | `method` | see below | Default `scale`. |
| Athlete mode | `athlete_mode` | yes/no | Defaults to your Settings default when left out in the API. |
| Weight | `weight_kg` | kg | |
| Body fat | `fat_ratio_pct` | % | |
| Fat mass | `fat_mass_kg` | kg | |
| Fat-free mass | `fat_free_mass_kg` | kg | |
| Muscle mass | `muscle_mass_kg` | kg | Stored; estimates use fat-free and lean mass instead. |
| Hydration | `hydration_kg` | kg | Body water. Never corrected. |
| Bone mass | `bone_mass_kg` | kg | Calibrated against DEXA bone mass. |
| Visceral fat | `visceral_fat_index` | index | The scale's unitless index. Never corrected. |
| Notes | `notes` | text | Up to 500 characters. |
| Source | `source` | text | Where the reading came from: `manual` (default), `withings` or any text you send by API. |
| External id | `external_id` | text | Your own stable id. Readings with an external id are upserted on (source, external id), so repeated imports do not duplicate. |

The gray calculated values (see [above](#auto-fill)) work on this form too. Deleting a reading cannot be undone.

### Measurement method {#measurement-method}

Each reading records how it was measured. The key goes in the API `method` field.

| Key | Label | Default rank |
| --- | --- | --- |
| `hydrostatic` | Hydrostatic weighing | 1 (most accurate) |
| `bodpod` | BodPod | 2 |
| `caliper` | Skinfold caliper | 3 |
| `scale` | Smart scale (Withings, manual) | 4 |
| `tape` | Tape / Navy formula | 5 |
| `visual-ai` | Visual AI | 6 |
| `manual-entry` | Typed into an app (unknown method) | 7 (least trusted) |

Readings of different methods are never mixed: each method (and athlete mode) is calibrated against DEXA on its own. See [Method accuracy](03-estimates.md#method-accuracy). A re-import never changes the method or athlete mode of a stored reading. A Withings import marks values that were typed into the Withings app (instead of measured by the scale) as `manual-entry`, so they do not distort the scale's own calibration; rank or ignore that method as you see fit.

### Athlete mode on a reading {#scale-athlete}

Tick **Athlete mode** when the scale was set to its athlete setting (which changes how it estimates fat). The box starts out as your default from Settings. See [Athlete mode](03-estimates.md#athlete-mode) for the effect.
