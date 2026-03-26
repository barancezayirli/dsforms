# Session 9 — Users & Account Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** User CRUD (list, create, delete with guards), password change with session invalidation, warning banner verification — completing the admin user management UI.

**Architecture:** `internal/handler/users.go` holds a `UsersHandler` with ListUsers, NewUserPage, CreateUser, DeleteUser, AccountPage, UpdatePassword. Templates `users.html` and `account.html` extend base.html. UpdatePassword calls `store.DeleteUserSessions(userID)` after password change to force re-login everywhere (leveraging the DB sessions refactor). DeleteUser prevents deleting self (checked via auth context) or last user (checked via store).

**Tech Stack:** Go `html/template`, chi router, existing store/auth/flash packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `templates/users.html` | Users table + add user form |
| `templates/account.html` | Change own password form |
| `internal/handler/users.go` | UsersHandler: 6 methods |
| `internal/handler/users_test.go` | 17 tests for all user management |
| `main.go` | Wire 6 user routes in protected group |

---

### Task 1: Create templates (users.html + account.html)

**Files:**
- Create: `templates/users.html`
- Create: `templates/account.html`

**users.html** extends base.html. Two sections:
1. Page header "Users" + "+ Add user" link
2. Users table: Username, Created, Actions (Delete button, disabled if self or last user)
3. Each user row shows "(you)" tag next to current user's name

**account.html** extends base.html:
1. Page header "Account Settings" with username subheading
2. Form: Current Password, New Password, Confirm New Password
3. Error message display

Commit templates first.

---

### Task 2: Users handler TDD (17 tests + implementation)

**Files:**
- Create: `internal/handler/users.go`
- Create: `internal/handler/users_test.go`

### UsersHandler struct:

```go
type UsersHandler struct {
    Store     *store.Store
    SecretKey string  // for flash messages
    BaseURL   string
    Templates map[string]*template.Template
}
```

### 17 Tests (matching DSFORMS_SESSIONS.md):

```
TestListUsersReturns200 — GET /admin/users → 200, contains "admin"
TestListUsersCurrentUserTag — response contains "(you)" for current user
TestCreateUserValid — POST with username+password+confirm → 302, user exists in store
TestCreateUserDuplicate — POST with existing username → 200, error message
TestCreateUserMismatchedPasswords — POST with password != confirm → 200, error
TestCreateUserEmptyUsername — POST with empty username → 200, error
TestNewUserPage — GET /admin/users/new → 200
TestDeleteUser — POST /admin/users/:id/delete → 302, user gone
TestDeleteUserSelf — POST delete with own ID → 302 + flash error, user still exists
TestDeleteUserLast — POST delete last user → 302 + flash error (or re-render)
TestDeleteUserNotFound — POST delete nonexistent → 404
TestAccountPage — GET /admin/account → 200
TestUpdatePasswordValid — POST correct current + matching new → 302 + flash success
TestUpdatePasswordWrongCurrent — POST wrong current → 200, error message
TestUpdatePasswordMismatch — POST new != confirm → 200, error message
TestUpdatePasswordClearsDefault — after update, IsDefaultPassword = false
TestUpdatePasswordInvalidatesSessions — after update, old session tokens are invalid
TestWarnBannerVisibleWhenDefault — admin page body contains "Default password" when IsDefaultPassword=true
TestWarnBannerAbsentAfterUpdate — admin page body does NOT contain "Default password" after password change
```

Note: 19 tests total (17 from session spec + 2 extras: TestNewUserPage, TestUpdatePasswordInvalidatesSessions).

### Handler methods:

**ListUsers(w, r):**
- Get current user from context
- List all users from store
- Mark current user with "you" flag
- Render users.html

**NewUserPage(w, r):**
- Render users.html with empty form (or a separate new user page)

**CreateUser(w, r):**
- Parse: username, password, confirm_password
- Validate: username non-empty, password non-empty, password == confirm
- Check duplicate: store.CreateUser returns error for UNIQUE violation
- On success: redirect to /admin/users with flash
- On error: re-render with error message

**DeleteUser(w, r):**
- Get user ID from URL param
- Get current user from context — if ID == current user ID → flash error, redirect
- Call store.DeleteUser(id) — returns error if last user
- On success: redirect to /admin/users
- On 404: return 404

**AccountPage(w, r):**
- Get current user from context
- Render account.html

**UpdatePassword(w, r):**
- Parse: current_password, new_password, confirm_password
- Validate: new_password == confirm_password
- Verify current password: store.CheckPassword(username, current_password)
- On wrong current: re-render with error "Current password is incorrect."
- Call store.UpdatePassword(userID, new_password) — sets IsDefaultPassword=false
- Call store.DeleteUserSessions(userID) — invalidate ALL sessions
- Create new session: store.CreateSession(userID, 30*24*time.Hour) — keep current user logged in
- Set new session cookie
- Flash "Password updated." and redirect to /admin/account

### Key implementation details:
- DeleteUser checks `currentUser.ID == deleteID` before calling store
- UpdatePassword creates a NEW session after deleting all old ones (so the user stays logged in)
- Warning banner test: render a page with IsDefaultPassword=true, check body contains "Default password"

---

### Task 3: Wire user routes in main.go

**Files:**
- Modify: `main.go`

Add UsersHandler creation + 6 routes in the protected group:
```go
r.Get("/admin/users", usersHandler.ListUsers)
r.Get("/admin/users/new", usersHandler.NewUserPage)
r.Post("/admin/users/new", usersHandler.CreateUser)
r.Post("/admin/users/{id}/delete", usersHandler.DeleteUser)
r.Get("/admin/account", usersHandler.AccountPage)
r.Post("/admin/account/password", usersHandler.UpdatePassword)
```

Add `users.html` and `account.html` to template cloning.

---

### Task 4: Final verification

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```
