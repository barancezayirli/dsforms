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

**API tokens are not in a snapshot.** They are cleared from the copy, and the
copy is rewritten so the hashes are gone from the file rather than merely
unlinked. Two reasons: a backup gets copied to laptops and object stores and had
no business carrying credential material, and — the sharper one — restoring a
snapshot used to bring back **every token revoked since it was taken**.
Revocation is a security action, and whoever you revoked may still be holding
the string.

The cost is the other side of that: **after restoring, your API tokens are
gone** and MCP clients stop working until you mint new ones. That is deliberate.
A client that visibly stops is a better failure than a revoked credential
quietly working again.

Login sessions *are* kept, so a restore does not sign everyone out. They expire
on their own, which tokens need not.

## What happens if a restore fails

A restore replaces the live database, so it is written to fail safely:

- The uploaded file is checked before anything is touched. A file that is not a
  valid dsforms database is refused and nothing changes.
- The existing database is set aside, not overwritten, until the replacement has
  actually opened. If it does not, the original is put back and stays in service.
- You are told which of those happened. *"That file was rejected"* means your
  database is untouched; *"your existing database is unchanged and still in
  use"* means the swap failed and was undone.
- In the one case where neither works, the message names the file your data is
  in (`<DB_PATH>.rollback`) and says not to restart before moving it back —
  starting with no database there creates an empty one.

If a restore is interrupted by the process dying, dsforms refuses to start
another one while `<DB_PATH>.rollback` exists, because that file may be your
only remaining copy.
