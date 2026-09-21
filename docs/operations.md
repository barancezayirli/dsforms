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
for the live database. Stop the server, then move the old database **and its
`-wal` and `-shm` files** aside before putting the new one in place: SQLite
replays a leftover `-wal` over whatever file it finds, which would write the old
database's last transactions into your replacement. Move rather than delete —
that `-wal` may hold writes the old database never checkpointed, and dsforms
installs no signal handler, so a container stop leaves them there.

Move all three into a directory together, keeping their names. SQLite finds a
log only at `<database>-wal`, so renaming them apart — `dsforms.db.old` beside
`dsforms.db-wal.old` — orphans the log and loses exactly the writes moving it
was meant to keep.

Restoring through the Backups page is the safer route and does none of this by
hand: it checkpoints the running database first and refuses outright if the log
cannot be flushed, then parks the old file until the replacement has opened.
That park is not an undo — it is deleted once the new database answers, so a
restore that succeeds on the wrong snapshot has nothing to go back to. Take
your own copy first.

**API tokens and login sessions are not in a snapshot.** They are cleared from
the copy, and the copy is rewritten so the hashes are gone from the file rather
than merely unlinked. Two reasons: a backup gets copied to laptops and object
stores and had no business carrying credential material, and — the sharper one —
restoring a snapshot used to undo revocation. A token you revoked, a session you
logged out of, a session cascaded away with a deleted user: all of them came
back and worked again.

They are stripped on the way in as well as on the way out, so restoring through
the Backups page cleans a snapshot taken by an older build, or a raw copy of a
database file, before anything is swapped.

**Whether a file is cleaned depends on how it gets there.** The stripping lives
in export and restore; starting the server on a database file does not clean it.
A snapshot from the Backups page was stripped when it was made, so copying it
into place is fine on this count — but a file you copied yourself from a live
database, or one from a build before any of this existed, still carries its
tokens and sessions, and putting it at `DB_PATH` by hand makes them work again.
Restore that kind of file through the Backups page instead, which strips it on
the way in. The upload is capped at 100MB and must be a SQLite database rather
than an archive — there is no decompression on that path.

One thing to know about where such a file came from: **`cp` is not a way to copy
a running SQLite database.** It takes the main file without the `-wal` beside
it, so at best it is whatever the last checkpoint left; it is not an atomic read
either, so it may instead be torn and refuse to open at all. The stale case is
the dangerous one, because nothing downstream can tell — it opens cleanly and
passes the integrity check. Use the Backups page, `dsforms backup create`, or
`VACUUM INTO` against the live database itself.

**A snapshot is still sensitive.** Nothing but those two tables is stripped, so
the file holds every submission, every waitlist entry with its email and IP,
every user's bcrypt password hash, and every form's webhook URL — which is
itself a credential for the Slack or Discord channel it posts to. Treat a
snapshot as you would the live database, not as something safe to pass around.

**And a restore is a whole-database replacement.** Everything the stripping does
not remove comes back as the snapshot had it: an account you deleted since
returns with its password hash, and a password you changed reverts to the old
one. Clearing tokens and sessions keeps those two out; it does not make a
restore safe to run without looking at what the snapshot predates.

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
