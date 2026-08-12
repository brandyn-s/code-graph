# Search + graph maintenance dogfood — 2026-08-12

The released code-search runtime and candidate code-graph binary were used
together on the public `Chainlit/chainlit` repository at
`8b2d4bacfd4fa2c8af72e2d140d527d20125b07b`. The checkout stayed clean. This
is a workflow probe, not an accuracy benchmark or superiority claim.

## Question 1: where does WebSocket authentication happen?

Natural-language `code_localize` ranked `backend/chainlit/auth.py` first and
also surfaced the session and OAuth implementation files. Structural search
then identified `socket.connect` at lines 97–158 and `auth.get_current_user` at
lines 93–97. A confidence-filtered directed trace established:

`connect → get_current_user → authenticate_user → get_jwt_secret`

The trace also linked session construction and login-policy checks. Direct
source inspection confirmed that `connect` extracts the bearer token,
`authenticate_user` verifies HS256 signature, and the resulting user/token are
passed into `WebsocketSession`.

## Question 2: how is reconnect handled?

Natural-language localization ranked `session.py` first and `socket.py`
second. The graph found exactly one inbound caller for
`restore_existing_session`: `connect`, confidence 0.9. Direct source inspection
confirmed the session-id header flows through this function before a new
session is created.

## Question 3: which endpoints depend on the current user?

Natural-language localization alone ranked client API code first and did not
directly answer the relationship question. The inbound graph trace did: it
returned ten callers of `get_current_user`, including project settings,
feedback, thread, file upload/serve, and WebSocket connect paths, all at
confidence 0.9 or 0.95.

## Finding

Composition worked as intended: semantic search localized concepts and the
graph verified direction and blast radius. It also exposed the honest division
of labor—conceptual search should not be treated as a call-relationship oracle,
and the heuristic Python graph still reports confidence rather than
compiler-grade certainty. No product change was justified by this bounded
probe.
