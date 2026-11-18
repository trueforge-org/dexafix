# Body service internals

How the body service behaves at its edges. Running it is described in [Body service](../admin/08-body-service.md).

## Limits

- A call is not atomic across the two services. An import is applied in steps
  that can be repeated, so a run that stops half way is finished by running it
  again.
- An export is built in memory in the body service and the api process.
- Backups of the two databases are separate: restore them from the same moment, or
  readings of users who were deleted in between wait for the next sweep.
