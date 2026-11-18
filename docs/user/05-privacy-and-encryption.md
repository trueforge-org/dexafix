# Encryption and privacy

These values are encrypted with AES-256-GCM before they are stored:

- each user's Withings tokens and Strength Level login
- sign-in provider secrets, including the Apple key
- Withings application credentials and every other server setting on the Admin
  page, including the SMTP password and the private key of an uploaded HTTPS
  certificate
- e-mail addresses
- each user's two-step verification secret, the key of their authenticator app
- each user's scale readings and DEXA scans: the measured values and the notes.
  The time of a measurement, its source, its method and the user it belongs to
  stay readable, because the database needs them to find and order rows.

Each value is tied to its row, so copying it to another row makes it unreadable.
The readings and scans have a key of their own, derived from the encryption key,
so a value from one group does not open with the key of the other. One key file
covers both.
Each e-mail address also gets a keyed hash, derived from the encryption key. The
hash makes addresses searchable and unique without decrypting them. Passwords,
access tokens, password reset links and two-step verification recovery codes are
stored only as hashes. Usernames and first and last names are not encrypted, but addresses are. Height is not
encrypted by the application either. For that, and for names, use disk or
database encryption. Without an encryption key nothing is encrypted; readings
and scans written then are encrypted at the next start with a key.

The administrator keeps the encryption key; see [The encryption key](../admin/10-encryption-key.md).
