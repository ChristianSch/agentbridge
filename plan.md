# AgentBridge session-restore hardening plan

This plan is self-contained and assumes no context beyond the current repository. It focuses on the remaining weirdness around restoring persisted sessions after AgentBridge restarts.

## Current behavior summary

AgentBridge persists sessions/history to:

- `$AGENTBRIDGE_STATE_DIR/sessions.json`, or
- `~/.local/state/agentbridge/sessions.json`, or
- `./.agentbridge-sessions.json` fallback

Restore is implemented mainly in `internal/app/manager.go`:

- `restorePersisted()` loads persisted state and starts/restores runtimes.
- `persistNow()` writes sessions and history.
- `startAgent()` starts Pi/Hermes child processes and launches stdout/stderr/wait goroutines.
- Terminal sessions should now be treated as use-once and restored only as exited history.

## Status

Phase 1 restore correctness is complete:

- ✅ Step 1: sessions are registered before agent processes can publish state.
- ✅ Step 2: restored terminals are always exited/use-once history, with deduped notices.
- ✅ Step 3: minimal Pi/Hermes restore policy is encoded.
- ✅ Step 5: persistence backups are limited to once per manager/process lifetime.
- ✅ Step 6: core restore tests were added in `internal/app/manager_restore_test.go`.

Remaining Phase 2 work:

- Step 4: rehydrate attachment metadata from the attachment store on restore.
- Step 7: update README restore language after attachment restore semantics are finalized.
- Optional/manual validation from the final checklist.

## Goals

1. Make restore deterministic and race-free. ✅
2. Make terminal restore behavior consistently “use-once / exited history”. ✅
3. Clarify and enforce when Pi/Hermes sessions resume versus remain read-only. ✅
4. Keep restored attachment metadata useful without leaking attachment paths to the browser.
5. Reduce persistence backup churn. ✅
6. Add focused tests for restore behavior. ✅

## Non-goals

- Do not redesign the full persistence format unless necessary.
- Do not change the external HTTP/WebSocket API except where needed for correctness.
- Do not restart terminal sessions after AgentBridge restarts.

---

## Step 1 — Remove start/restore registration races ✅

### Problem

`CreateAgent()` and `restorePersisted()` call `startAgent(p)` before `p` is always visible in `m.sessions`. `startAgent()` immediately launches goroutines (`readLoop`, `stderrLoop`, `waitLoop`). If the child exits or emits state very quickly, those goroutines can publish or call `setState()` before `m.sessions[id] = p`, causing lost state updates.

### Desired behavior

A session runtime must be registered in `m.sessions` before any child-process goroutine can publish state for that session.

### Implementation options

Prefer option A.

#### Option A: register first, then start

In `CreateAgent()`:

1. Build `p`.
2. Lock `m.mu`, insert `m.sessions[id] = p`, unlock.
3. Call `startAgent(p)`.
4. If `startAgent` fails, either:
   - remove the session from `m.sessions` and return error, or
   - mark it `error` and return the session.

For current API behavior, preserve existing semantics: if create fails, remove from `m.sessions` and return the error.

In `restorePersisted()`:

1. Construct `p`.
2. Insert into `m.sessions` before starting.
3. Start as needed.
4. On start failure, leave the session in `error` state.

Be careful not to double-insert in multiple branches.

#### Option B: split start into prepare/start goroutines

Refactor `startAgent()` so it starts the process but does not launch goroutines until after registration. This is more invasive; avoid unless needed.

### Acceptance criteria

- No branch in restore starts an agent before the runtime is present in `m.sessions`.
- Fast process exit during restore results in a visible `error` or `exited` state, not stale `starting`.
- `go test ./...` passes.

---

## Step 2 — Make terminal restore consistently use-once ✅

### Problem

`restorePersisted()` currently checks `persistedState == exited` before the terminal-specific branch. Already-exited terminal sessions are restored as exited, but skip the terminal-specific history notice:

> Terminal sessions are use-once and are not restarted after AgentBridge restarts.

### Desired behavior

All terminal sessions, regardless of persisted state, restore as exited history with the same user-visible notice.

### Implementation

In `restorePersisted()`:

1. After constructing `p` and normalizing timestamps/dir, handle `sess.Kind == core.AgentTerminal` before the generic `persistedState == core.StateExited` check.
2. Set `p.State = core.StateExited`.
3. Insert into `m.sessions`.
4. Call `m.addHistoryNotice(sess.ID, "Terminal sessions are use-once and are not restarted after AgentBridge restarts.")`.
5. `continue`.

### Acceptance criteria

- No terminal is restarted from persistence.
- Every restored terminal has an exited state.
- Every restored terminal history includes the use-once notice exactly once or at least without repeated duplicates.

---

## Step 3 — Decide and encode Pi/Hermes restore semantics ✅

### Current ambiguity

`restorePersisted()` currently does:

```go
if persistedState == core.StateExited {
    p.State = core.StateExited
    m.sessions[sess.ID] = p
    continue
}
```

This prevents Pi/Hermes sessions from resuming if they were persisted as `exited`, even when a native `resumeID`/`remoteID` exists.

That may be correct if `exited` means intentionally stopped/read-only. But it conflicts somewhat with the README language saying Pi/Hermes resume when native IDs are known.

### Proposed policy

Use explicit restore policy:

- Terminal: always restore as exited history.
- Pi/Hermes with no native resume ID: restore as exited/read-only history.
- Pi/Hermes explicitly deleted/killed by user: should not be in `sessions.json` at all, because `Kill(remove=true)` removes it.
- Pi/Hermes idle-stopped or process-exited but still present in persistence with a resume ID: resume on AgentBridge restart.
- Pi/Hermes persisted as `StateError`: restore as read-only/error unless a resume ID exists and the previous error was only process lifecycle. This may require more metadata; for now prefer conservative read-only for `StateError`.

### Minimal implementation

Replace the generic `persistedState == exited` early return with kind-aware logic:

- For `core.AgentTerminal`: handled by Step 2.
- For `core.AgentPi`/`core.AgentHermes`:
  - If `resumeID == ""`: exited/read-only.
  - If `persistedState == core.StateError`: keep `error`/read-only unless you intentionally want retry.
  - Otherwise attempt resume, even if persisted state was `exited`.

Add a history notice when restoring read-only:

- `"Native session could not be resumed; restored AgentBridge history read-only."`

### Optional better implementation

Add a field to `persistedSession`, e.g.:

```go
StoppedReason string `json:"stopped_reason,omitempty"`
```

Values:

- `"idle_timeout"`
- `"user_deleted"` — probably not persisted because deleted sessions are removed
- `"resume_unavailable"`
- `"terminal_use_once"`

This gives precise future behavior, but is more invasive.

### Acceptance criteria

- Pi/Hermes sessions with native resume IDs resume after restart unless clearly read-only/error.
- Pi/Hermes sessions without resume IDs restore as read-only history.
- Terminal sessions never resume.
- README accurately describes this.

---

## Step 4 — Rehydrate attachment metadata on restore

### Problem

`publicAttachments()` strips sensitive fields before history persistence:

- `OwnerID`
- `Path`
- `Content`

Restore reconstructs attachment manifests from history via:

```go
attachmentIndex: attachmentsFromHistory(st.History[sess.ID])
```

Those attachments no longer have local `Path`, so the Pi attachment extension may list old attachments but cannot provide paths to agents after AgentBridge restart.

### Desired behavior

Browser-visible history should keep paths stripped, but restored agent attachment manifests should use full attachment metadata from the attachment store when available.

### Implementation approach

The `Manager` currently does not have an `AttachmentStore`. Options:

#### Option A: pass AttachmentStore into Manager

1. Add `attachments core.AttachmentStore` to `Manager`.
2. Change `NewManager(...)` signature to accept it, or add an option/setter.
3. In `cmd/agentbridge/main.go`, create `attachments` before `NewManager` and pass it in.
4. In restore, build `attachmentIndex` by:
   - collecting attachment IDs from history,
   - calling `attachments.Get(ctxWithOwner, id)` for each.

Need owner context. Since persisted sessions include `OwnerID`, use:

```go
ctx := core.WithOwnerID(context.Background(), sess.OwnerID)
```

If an attachment cannot be found, keep the stripped history metadata as fallback.

#### Option B: keep Manager ignorant, add manifest refresh elsewhere

Less clean. Avoid.

### Notes

- Do not send `Path` back to browser in REST/WebSocket history.
- It is okay for the agent-side manifest to include `Path`, because the agent process has local filesystem access and this is documented.

### Acceptance criteria

- After restart, attachments from previous user messages can still be found by `attachment_read` with local paths when attachment files still exist.
- Browser history still does not expose `Path` or `Content`.
- Missing attachment files do not break session restore.

---

## Step 5 — Reduce persistence backup churn ✅

### Problem

`persistNow()` currently calls `backupPersistedState(m.persistPath)` before every rename/write. Persistence can happen frequently, causing unnecessary backup churn.

### Desired behavior

Backups should protect against migration/corruption, not be created for every normal event write.

### Implementation

Add a field to `Manager`:

```go
persistBackedUp bool
```

In `persistNow()`:

- Only call `backupPersistedState` once per process lifetime, before the first successful overwrite.
- Set `persistBackedUp = true` after attempting backup.
- Guard with `persistMu` or a separate mutex because `persistNow()` can be invoked by timers.

Pseudo:

```go
m.persistMu.Lock()
if !m.persistBackedUp {
    backupPersistedState(m.persistPath)
    m.persistBackedUp = true
}
m.persistMu.Unlock()
```

Do this before `os.Rename(tmp, m.persistPath)`.

### Acceptance criteria

- Normal chat activity does not create a new `.bak-*` file every persist.
- At most one backup is created per process run.
- Existing pruning remains as a safety net.

---

## Step 6 — Add restore-focused tests ✅

### Suggested tests

Put tests in `internal/app/manager_restore_test.go`.

Use temp state directories via `t.Setenv("AGENTBRIDGE_STATE_DIR", tmp)`.

Possible tests:

1. **Terminal restore is use-once**
   - Write a `sessions.json` containing a terminal session with state `running`.
   - Construct manager.
   - Assert restored session exists and state is `exited`.
   - Assert history contains use-once notice.

2. **Exited terminal still gets notice**
   - Same as above but persisted state `exited`.
   - Assert notice exists.

3. **Pi/Hermes without resume ID restore read-only**
   - Persist agent session with no `ResumeID`/`RemoteID`.
   - Construct manager with a fake adapter if needed.
   - Assert state is `exited` and no process start attempted.

4. **Agent with resume ID registers before start**
   - Use a fake adapter command that exits immediately, e.g. `/bin/false` or a test helper process.
   - Ensure restored session is not stuck forever in stale `starting` due to registration race.

5. **Persistence backup once**
   - Trigger multiple `persistNow()` calls.
   - Assert only one `.bak-*` backup appears for the process run.

### Acceptance criteria

- Tests fail on the current race/terminal-notice issues before fixes.
- Tests pass after implementation.
- Full `go test ./...` passes.

---

## Step 7 — Documentation updates

Update README restore language to be precise:

- Pi/Hermes sessions resume after restart only when a native resume ID is available and the session is not read-only/error.
- Sessions without native resume IDs restore as read-only history.
- Terminal sessions are use-once shells and restore only as exited history.
- Attachments remain available to agents after restart when their stored files still exist.

---

## Final validation checklist

Run:

```sh
go test ./...
make frontend
```

Then re-run:

```sh
go test ./...
```

Manual smoke test:

1. Start AgentBridge with a temp state dir.
2. Create a terminal session.
3. Restart AgentBridge.
4. Confirm terminal appears exited/use-once and does not spawn a shell.
5. Create Pi/Hermes session, send prompt, ensure resume ID is captured if available.
6. Restart AgentBridge.
7. Confirm Pi/Hermes resumes when resume ID exists, otherwise shows read-only history.
8. Upload an attachment, send it to an agent, restart, and confirm the attachment tool can still identify it if the file exists.
