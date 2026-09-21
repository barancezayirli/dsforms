# Operations

## Managing users

From the admin UI: **Users** to add or remove people, **Account** to change your
own password. Changing a password signs out every session on every device,
including the current one.

Passwords are bcrypt at cost 12, minimum 12 characters, enforced in the store so
it applies to every path that sets one.

From the command line — useful when you are locked out:

```bash
# List users
docker compose exec dsforms ./dsforms user list

# Add a user
docker compose exec dsforms ./dsforms user add alice secretpassword

# Reset a password
docker compose exec dsforms ./dsforms user set-password admin a-new-passphrase

# Delete a user
docker compose exec dsforms ./dsforms user delete alice
```

The seeded `admin` / `admin` account is written directly rather than through the
normal path, which is why it is shorter than the minimum and why the first run
warns you to change it.

## Backups

From the admin UI: **Backups** downloads a full snapshot, or restores from one.

From the command line:

```bash
# Set BACKUP_LOCAL_DIR in .env first
docker compose exec dsforms ./dsforms backup create
```

The snapshot is a standard SQLite file taken with `VACUUM INTO`, so it is safe
to take while the server is running and it is compacted — expect it to be
smaller than the figure the Backups page reports, which is the on-disk footprint
including the write-ahead log.

You can open it with any SQLite client, or drop it in as a direct replacement
for the live database.

**API tokens and login sessions are not in a snapshot.** They are cleared from
the copy, and the copy is rewritten so the hashes are gone from the file rather
than merely unlinked. Two reasons: a backup gets copied to laptops and object
stores and had no business carrying credential material, and — the sharper one —
restoring a snapshot used to undo revocation. A token you revoked, a session you
logged out of, a session cascaded away with a deleted user: all of them came
back and worked again.

They are stripped on the way in as well as on the way out, so a snapshot taken
by an older build, or a raw copy of a database file, cannot walk them back in
either.

**A snapshot is still sensitive.** Those two tables are what a restore could
walk back in; nothing else is stripped, so the file still holds every
submission, every user's bcrypt password hash, and every form's webhook URL —
which is itself a credential for the Slack or Discord channel it posts to. Treat
a snapshot as you would the live database, not as something safe to pass around.

The cost is the other side of that. **After restoring, your API tokens are gone
and everyone is signed out, including you.** MCP clients stop working until you
mint new tokens. That is deliberate: a client or a person visibly stopping is a
better failure than a revoked credential quietly working again — and the
operator performing a restore was always signed out by it anyway, since their
own session postdates the snapshot.

## What happens if a restore fails

A restore replaces the live database, so it is written to fail safely:

- The uploaded file is checked before anything is touched. A file that is not a
  valid dsforms database is refused and nothing changes.
- The existing database is set aside, not overwritten, until the replacement has
  actually opened. If it does not, the original is put back and stays in service.
- You are told which of those happened. *"That file was rejected"* means your
  database is untouched; *"your existing database is unchanged and still in
  use"* means the swap failed and was undone.
- A restore can also be refused because this instance was not in a state to
  accept it — a parked database from a restore that did not finish, a
  write-ahead log another request is holding open, a disk with no room. Your
  database is untouched here too, and the message says the file is not the
  problem, because the obvious next step otherwise is to re-export and re-upload
  the one thing that was already fine. The server log names the obstacle.
  Where a failure could be either — the disk filling up while the upload is
  being read or cleaned, say — dsforms goes by the reason SQLite gives. When
  that reason does not say, it reports the file as rejected rather than guess:
  so if re-uploading fails the same way, the server log is the place to look.
- In the one case where neither works, the message names the file your data is
  in (`<DB_PATH>.rollback`) and says not to restart before moving it back —
  starting with no database there creates an empty one.

If a restore is interrupted by the process dying, dsforms refuses to start
another one while `<DB_PATH>.rollback` exists, because that file may be your
only remaining copy.
