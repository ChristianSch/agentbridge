# AgentBridge

Unified remote UI for Pi, Hermes, and terminal sessions.

## Architecture

Ports and adapters layout:

- `internal/core`: domain types and ports
- `internal/app`: session/process manager use cases
- `internal/adapters/agent`: Pi and Hermes protocol adapters
- `internal/adapters/http`: REST and WebSocket transport
- `internal/static`: embedded frontend assets

## Run locally

```sh
cp agentbridge.yaml.example agentbridge.yaml
AGENTBRIDGE_TOKEN=dev go run ./cmd/agentbridge --config ./agentbridge.yaml 2>&1 | tee agentbridge.log
```

Open:

```text
http://127.0.0.1:7777/?token=dev
```

Build a binary:

```sh
make frontend
make server
./agentbridge --config ./agentbridge.yaml
```

## Current status

Implemented:

- Token auth for REST and WebSockets
- REST health/projects/session endpoints
- Multiplexed agent WebSocket at `/ws`
- Terminal WebSocket at `/ws/term/:id`
- Multi-session manager for Pi, Hermes, and terminal sessions
- PTY terminals via `github.com/creack/pty`
- Pi protocol adapter. Current implementation starts `pi --mode rpc` with the session cwd as the child process working directory.
- Hermes JSON-RPC adapter with `session.create` and `session.resume`
- Agent restart with exponential backoff
- Idle reaper for sessions with no clients
- Session event history replay on subscribe
- Session/history persistence across AgentBridge restarts (`~/.local/state/agentbridge/sessions.json`, or `$AGENTBRIDGE_STATE_DIR/sessions.json`), including Hermes `session.resume` and Pi `--session` resume when remote session IDs are known
- Separate stderr event capture
- ntfy notification hook for waiting/approval events
- Preact/xterm frontend with session sidebar, agent chat, terminal panes, and approval buttons

## Useful API

```sh
curl -H "Authorization: Bearer dev" http://127.0.0.1:7777/api/health
curl -H "Authorization: Bearer dev" http://127.0.0.1:7777/api/sessions
```

Create a terminal:

```sh
curl -X POST -H "Authorization: Bearer dev" -H 'Content-Type: application/json' \
  -d '{"kind":"terminal","name":"shell","cwd":"/tmp"}' \
  http://127.0.0.1:7777/api/sessions
```

Create Hermes resume session:

```sh
curl -X POST -H "Authorization: Bearer dev" -H 'Content-Type: application/json' \
  -d '{"kind":"hermes","name":"general","cwd":"/home/user","resume_id":"SESSION_ID"}' \
  http://127.0.0.1:7777/api/sessions
```
