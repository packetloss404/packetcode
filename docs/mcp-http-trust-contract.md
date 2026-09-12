# Streamable HTTP MCP Trust Contract

Status: **approved design gate, 2026-08-01**

Contract: `packetcode-mcp-http-trust-v1`

Transport status: **not enabled**

This is the security contract PacketCode must implement before it accepts a
Streamable HTTP MCP server in configuration. It closes PCH5's design gate; it
does not add a URL field, transport switch, HTTP client, or network listener.
The executable remains stdio-only.

The governing rule is fail closed: each server states every trust decision,
and a missing, malformed, unresolved, or unsupported decision prevents that
remote server from starting. PacketCode itself and unrelated local MCP servers
continue to work.

## 1. Target and network allowlist

Each server will declare one endpoint URL and an explicit origin allowlist.
An origin is the exact tuple `scheme://host:port`; the port is mandatory even
when it is `443` or `80`. URL userinfo, fragments, and endpoint query strings
are forbidden. Credentials never belong in a URL.

PacketCode classifies every literal or DNS-resolved address as exactly one of:

| Class | Examples | Trust meaning |
| --- | --- | --- |
| `loopback` | `127.0.0.1`, `::1` | This machine only. |
| `private` | RFC 1918, IPv6 unique-local | Controlled private network; not equivalent to loopback. |
| `link-local` | `169.254.0.0/16`, `fe80::/10` | Direct-link services and metadata-adjacent space; separate explicit decision. |
| `reserved` | documentation, benchmark, CGNAT, translation, transition, unspecified, and other special-use ranges | Not ordinary public routing; separate explicit decision. |
| `public` | ordinary globally routable addresses | Hosted service trust boundary. |

The server contract lists allowed address classes. A literal IP must match one
of them. A DNS name is not trusted merely because its text is allowlisted:
PacketCode must resolve it for each new connection and classify every candidate.
Every address in the answer must be allowed; a mixed-trust or empty answer
rejects the whole connection attempt rather than selecting a convenient member.
This includes DNS rebinding from public to private/loopback/link-local/reserved
space. IPv6 classification conservatively treats addresses outside the normal
`2000::/3` global-unicast allocation, plus special ranges inside it such as
6to4 and documentation space, as `reserved`; tests pin translation, discard,
dummy, transition, and documentation examples from the
[IANA IPv6 special-purpose registry](https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry.xhtml).

Plain HTTP requires an explicit `allow_plain_http` decision and can never be
combined with the `public` class. HTTPS is required for public endpoints.
HTTPS uses the platform/system root store with ordinary certificate and
hostname verification; custom roots and `InsecureSkipVerify` are not v1
options. The HTTP variant explicitly selects `tls = "none"`.

Ambient proxies are disabled. PacketCode must not honor `HTTP_PROXY`,
`HTTPS_PROXY`, `ALL_PROXY`, or process-level proxy callbacks for remote MCP.
A future proxy feature needs its own exact origin/address, TLS, authentication,
DNS-resolution, and credential-forwarding contract.

The reviewed Go validator is `internal/mcp/http_trust.go`. The future config
shape remains deliberately unimplemented until the transport loop.

## 2. Redirect policy

Redirect behaviour is explicit per server and has two values:

- `deny` — do not follow redirects.
- `same-origin` — follow only a bodyless `GET` or `HEAD` when the exact scheme,
  host, port, and method remain the same. The server declares a hard hop cap
  from one through five.

PacketCode must disable ambient HTTP-client redirects and evaluate every hop.
Both the source and destination origin, redirect status, one-based hop count,
previous/next method, and both request-body states are checked. DNS/address-
class checks run again before the next connection, and an unlisted origin
terminates the request. Any redirect after a Streamable HTTP `POST`, any method
rewrite, and any next request with a body is refused—including body-preserving
307/308 responses—so an approved tool call cannot be replayed or duplicated by
redirect handling.

## 3. Credentials

The v1 credential mode is explicit: `none` or `bearer-env`. Inline credential
values are forbidden. `bearer-env` names one environment variable; its value
becomes an `Authorization: Bearer` header only on requests to the original
target origin. `ValidatedRemoteHTTPTrust.BindRuntime` must successfully bind
the resolved, 16–4096-byte, header-safe ASCII bearer value before authenticated
request construction; the returned runtime object owns both credential-
attachment checks and output sanitization, so those capabilities cannot be
initialized independently.

Cross-origin redirects are refused rather than attempting to strip and replay
requests. Redirected same-origin requests are constructed deliberately rather
than inheriting ambient cookie/proxy state. Credential values and
credential-bearing URLs are redacted from diagnostics, logs, errors,
transcripts, and model-facing tool output.

## 4. Output provenance

All remote MCP output is untrusted evidence. Before it reaches the session,
conversation view, or model, PacketCode must:

1. redact credential-shaped values and the exact resolved bearer value (plus
   mixed/partial percent encoding, JSON escapes, and common base64 variants of
   that value);
2. wrap the content in a labelled boundary naming the server and origin; and
3. keep it in a `tool` role associated with the originating tool call.

It must never be appended as a system, developer, policy, or other instruction
message. Text inside the boundary can be quoted or analyzed, but statements
such as “ignore previous instructions” do not gain instruction authority.

`ValidatedRemoteHTTPTrust.BindRuntime` atomically binds the v1 boundary and
credential-attachment decision to the validated server and resolved secret,
and refuses a missing credential in `bearer-env` mode.
`mcp.RedactSensitiveText` remains the heuristic layer shared with user-visible
MCP log tails. Regression tests contain prompt-injection-shaped text,
naked/partially-percent-encoded/JSON-escaped opaque bearer values, base64
variants, bearer/basic headers, cookies, JSON tokens, username-only URL
userinfo, encoded query keys, and secret query parameters.

## 5. Approval scope and revocation

Remote MCP tools remain approval-gated. The contract's only supported automatic
scope is `call`: approving one invocation approves that invocation only. There
is no implicit server-wide approval and no trust inherited by another server or
origin.

The existing “Allow this tool this session” choice is a separate, deliberate user
action. It remembers the exact provider-safe tool alias for the current
PacketCode session, not the whole server. `/permissions` shows the resulting
rule. `/permissions reset` revokes remembered/session rules, exits temporary
Plan or Bypass state, and restores the startup permission configuration.
PacketCode restart also clears session rules.

Explicit configured `deny` rules remain the safety floor. The first transport
implementation must not add a remote-only bypass or a server-wide remember
button. It also must not forward remote MCP adapters into background-job
registries: current jobs deliberately omit MCP/unknown tools because they
cannot clone their workspace and trust boundary. Background remote access needs
a separate workspace, approval-snapshot, cancellation, and revocation design.

## 6. Failure and reconnect semantics

The v1 reconnect mode is `manual` with zero automatic reconnect attempts.
Timeouts must be explicitly between one second and five minutes. Timeout,
connection loss, protocol failure, and server closure produce a tool-role error
and a visible server status; they do not silently retry, switch origin, or
reinterpret partial output as success.

A user may request an explicit reconnect through the MCP recovery surface. The
same validated endpoint, origin allowlist, address classes, redirect policy,
and credential rules are applied again. A configuration change requires a new
PacketCode start; reconnect never discovers or adopts a different endpoint.

Every server also declares bounded response, SSE event, header, and final
model-facing output byte limits. Validation caps them at 32 MiB, 1 MiB,
128 KiB, and 1 MiB respectively, with smaller positive minima and internal
cross-checks. Limits are enforced while reading, not after buffering.
Compression mode is explicitly `identity`; automatic decompression is disabled
so compressed data cannot expand past a post-read check. Sanitized tool output
retains its trust boundary and truncation marker within the configured output
cap. Exact-value matching uses a bounded linear streaming matcher rather than
credential-sized regular expressions; raw sanitizer input is pre-bounded to
the output cap plus the longest supported encoded-secret lookahead. Regression
coverage exercises a maximum-length near-match credential against a maximum-
size hostile response.

## Implementation gate

A Streamable HTTP implementation may open only if it preserves all of these
properties:

- no network request occurs before the entire per-server contract validates;
- dialing and every redirect hop use the validator's origin, address, method,
  body, status, and hop-count checks;
- the standard client cannot use ambient proxies or unsafe TLS configuration;
- cross-origin redirects, POST redirects, method rewrites, and request-body
  replay are refused;
- credential attachment uses the exact target-origin rule only after atomic
  runtime credential/output binding succeeds;
- remote content uses the credential-bound, labelled, redacted, bounded
  tool-output sanitizer;
- response, event, header, compression, and model-output limits are enforced
  incrementally;
- call approval and `/permissions reset` revocation remain test-covered;
- timeouts and disconnects surface without automatic reconnect or origin drift;
- `packetcode doctor --check mcp` reports the redacted contract and fails an
  invalid remote definition without contacting it; and
- stdio MCP behaviour and failure isolation remain unchanged.

The transport loop must add integration tests with a local adversarial HTTP
fixture covering redirect/body-replay escape and hop exhaustion, DNS/address-
class rejection, proxy bypass, TLS failure, credential non-forwarding and
partially encoded exact-value echo, oversize labels/responses, compressed
responses, injected output, timeout, disconnect, and explicit reconnect.
