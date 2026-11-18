# Withings and Strength Level

## Withings {#withings}

Each user links their own Withings account. Imported measurements are stored
as scale readings with the source `withings` and the athlete-mode default from
Settings. An administrator sets Withings up first: see
[Withings application](../admin/09-the-admin-page.md#withings-app) and
[Withings workers and automatic imports](../admin/09-the-admin-page.md#withings-workers).

### Connecting and importing {#withings-import}

1. On the Settings or Estimates page choose **Connect Withings** and grant
   access. Finish within 10 minutes, the lifetime of the connection state.
2. Choose **Import from Withings**. It fetches the whole history.

Connecting works only when the administrator has set the client id, secret and
redirect URL; until then Settings shows Withings as not set up. Talking to
Withings is a job for a worker, so imports wait while no worker runs. Automatic
imports are off until an administrator gives them an interval; after the number
of failed runs in a row that the administrator set, your automatic imports
pause, until you disconnect and connect Withings again or an administrator
resumes them.

Imported: weight, fat ratio, fat mass, fat-free mass, muscle mass, hydration,
bone mass and visceral fat. Measurements Withings marks as possibly belonging
to someone else (for example a guest) are ignored. Days are calendar days in
your [time zone](01-using-the-app.md#time-zone).

**Disconnect Withings** forgets your tokens. Imported readings are kept.

### Bulk import

**Import from Withings** fetches the whole history. Each day is handled by what
is already stored:

| Day has                | What happens                                                                                          |
| ---------------------- | ----------------------------------------------------------------------------------------------------- |
| No readings            | Imported first.                                                                                       |
| Withings readings only | Changed readings are updated, identical ones are left alone, new ones are added. Nothing deleted.     |
| Manual readings        | New Withings readings are skipped and the day is listed in the result. Earlier imports are refreshed. |

A manual entry is a deliberate value. Averaging it with a Withings reading of
the same weigh-in would distort the day, so bulk import leaves such days alone.

### Re-import one day

Use **↻** on a row of the estimates page, or `?day=` on the API, to re-import
one day. It replaces that day's Withings data:

- Adds the day's Withings readings, even when manual readings exist.
- Updates changed readings.
- Removes stored Withings readings that Withings no longer returns, for
  example after a weigh-in was deleted in the Withings app.
- Never touches manual readings.

## Strength Level {#strengthlevel}

Sends your estimated weight and body fat to your
[Strength Level](https://strengthlevel.com) account, so its strength standards
use your real body weight.

- **Permission:** an administrator must enable *Strength Level sync* for you on
  the Admin page ([Strength Level permissions](../admin/09-the-admin-page.md#strengthlevel-permissions)).
  Until then the card does not appear and the endpoints answer 403.
- **Login:** your Strength Level e-mail and password, stored encrypted. Only
  e-mail and password accounts work, not Google, Apple or Facebook sign-in. The
  login is tested when you save it, so a wrong password is reported at once. The
  password is never shown again. **Remove login** deletes it.
- **Single day:** the **⇪** button on a row of the Estimates page sends that day
  and replaces an entry that is already there.
- **Multi-day sync** (needs the extra *multi-day sync* permission): choose
  *Last N days* (default 90, 1 to 3650). Days missing on Strength Level are
  added. With *Also update days that already exist* ticked, days that exist
  there with other values are overwritten too. Nothing is ever deleted. It runs
  in the background, one day at a time with a short pause, so long periods take
  a while; the progress shows on the page. Only one sync per user runs at a
  time.
- **What is sent:** the daily estimate of weight and body fat, including
  interpolated and held days. Days without a weight estimate are left out.
