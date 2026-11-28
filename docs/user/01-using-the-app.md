# Using the app

## Getting started

DexaFix corrects smart-scale results with DEXA scan results. A smart scale is
convenient but biased. A DEXA scan is accurate but rare. The app uses the
difference between the two on the days you have both, and applies it to the
scale readings in between. You get one estimated value per day.

1. Create the first account. On a new server it becomes the administrator.
   After that, new accounts are closed until an administrator allows sign-ups
   (see [Signing up](02-accounts-and-sign-in.md#registration)).
2. Enter your height under Settings (needed for BMI, FMI, LMI, FFMI and the Physique Score).
3. Add one or more [DEXA scans](06-scans-and-readings.md#dexa-page).
4. Add [scale readings](06-scans-and-readings.md#scale-page) by hand, by the
   [JSON API](../developer/02-json-api.md), or by importing them from
   [Withings](04-withings-and-strength-level.md#withings). A DEXA scan only
   calibrates a scale if the scale has a reading on the same day.
5. Open the estimates page to see the daily table, the chart and the
   calibrations in use.

Stored readings are never changed. Every estimate is calculated from them, so
you can change settings at any time and see the result at once. Small **?**
badges throughout the app link to the matching section of this documentation.

## The pages

Settings and Admin are in a menu under your first name (or your username, when
you have not set one) in the header, which also has **Sign out**.

- `/`: the home page for visitors: a short introduction next to the sign-in
  page (the sign-in page comes first on a phone). Sign in with an e-mail address and password, with a
  passkey (**Sign in with a passkey**), create an account, or use a provider
  button. A username is unique (ignoring case) and has 2 to 32 letters, digits,
  `.`, `_` or `-`, starting with a letter or digit. You sign in with your
  e-mail address, not your username.
- `/u/{id}`: redirects to the estimates page.
- `/u/{id}/estimates`: one row per day with the estimated values, an overview
  with the latest values and a trend chart, the calibrations in use, and the
  Withings buttons.
- `/u/{id}/dexa`: add and delete DEXA scans. A scan needs a date and at least
  one measurement.
- `/u/{id}/scale`: add and delete readings. Times are in your time zone (Settings). Choose the
  measurement method in the drop-down and tick **Athlete mode** if it applies.
- `/u/{id}/settings`: the athlete-mode default and the accuracy rank of each
  method. The ranking is a drag-to-reorder list, with up and down buttons for
  keyboard use. Without JavaScript it has rank number inputs instead. A Withings
  section connects, imports from and disconnects the user's Withings account. It
  says so when Withings credentials are missing, either on the Admin page or in
  the environment. Also:
  - **Profile**: username, first name and last name (both optional, up to 64
    characters), e-mail, time zone and height in cm. The e-mail is
    required, because it is how you sign in; changing it asks for your current
    password. The time zone (an IANA name such as `Europe/Amsterdam`, default
    `UTC`) decides which calendar day a scale reading belongs to, how the date
    and time of a reading are read and shown, what "today" is and which day a
    Withings import covers. DEXA scans are plain dates and are not moved.
  - **Access tokens**: create and revoke personal access tokens for the API.
  - **Linked accounts**: link or unlink sign-in providers (unlinking asks for your current password, or a recent sign-in when you have none).
  - **Two-step verification**: turn it on or off, and replace your recovery codes.
    See [Two-step verification](02-accounts-and-sign-in.md#two-step).
  - **Passkeys**: add a passkey, rename it or remove it. See
    [Passkeys](02-accounts-and-sign-in.md#passkeys-doc).
  - **Password**: set or change the account's password. Changing it signs out
    the other sessions and revokes all access tokens.
- `/admin`: administrators only. See [The Admin page](../admin/09-the-admin-page.md).

Numbers may use a decimal comma. Notes are limited to 500 characters.

While a scale reading or DEXA scan is being entered, an empty field shows a gray
value when it can be calculated from what was typed. Weight, body fat %, fat
mass and fat-free mass calculate from each other: any two give the rest. On the
DEXA form, lean mass + bone mass gives fat-free mass. A typed value is never
replaced. Gray values are saved with the form unless a value is typed instead.
This needs JavaScript; without it the fields stay empty.

On the estimates page, a `DEXA` badge marks a value from a DEXA scan, and a
`corr.` badge marks a corrected value. Faded values are interpolated or held,
and a gray `est` tag in front of the date marks a row with a value
interpolated between two readings of that field. Hovering a value shows its
origin, the method and mode it is based on, and the raw reading. The
calibrations section lists each scan's offsets (DEXA minus the reading on the
scan day) per method and mode. Days with only a DEXA scan appear too. The
daily table shows the same period as the chart (30 days, 90 days, 1 year or
all time, 90 days by default). The Columns menu above the daily table shows
and hides columns, and the choice is remembered per account.

## Settings

### Profile {#profile-settings}

Username (unique), first and last name (both optional), e-mail (required, it is
how you sign in; changing it asks for your current password) and height in cm
(50 to 280, optional). Height is needed for BMI, FMI, LMI, FFMI and the Physique Score.

### Time zone {#time-zone}

Your IANA time zone, such as `Europe/Amsterdam` (default `UTC`). It decides
which calendar day a scale reading belongs to, how the date and time you type
for a reading are read and shown, what "today" is, and which day a Withings
import covers. DEXA scans are plain dates and are not moved by it. Changing it
re-groups your readings into days, so the estimates are recalculated.

### Password {#password}

Set or change your password (current password required when you have one).
Shown only while password sign-in is on. Minimum 8 characters, at most 72
bytes. See [Password sign-in](02-accounts-and-sign-in.md#password-login).

### Recent account activity

The latest security events on your account (time in UTC, event, who did it,
detail, IP address). If you see something you do not recognise, change your
password and revoke your access tokens.

## The footer, terms and privacy

Every page has a footer with links to the documentation, the status page, the
**Terms of Service** (`/terms`), the **Privacy Policy** (`/privacy`), the
project's sponsor page on [Open Collective](https://opencollective.com/dexafix)
and, as a GitHub icon, the source code (which the AGPL asks a server to offer).
Signed-in people see a short, dismissible reminder above the estimates that the
project lives on support; closing it is remembered for 90 days in a cookie.
