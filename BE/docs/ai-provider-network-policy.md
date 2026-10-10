# AI provider network policy

Workspace owners can configure OpenAI-compatible provider URLs. The server
permits connections to public network addresses by default. It blocks private,
loopback, shared-address/CGNAT, link-local/metadata, unspecified, multicast,
documentation, benchmarking, reserved, and address translation/tunneling ranges.
IPv4-mapped IPv6 addresses follow the IPv4 rules. The IPv6 public boundary is
global unicast `2000::/3`, excluding special-use ranges.

The production contract requires a fixed safe provider policy and automatic
provider discovery, with vendor keys stored once in OCI Vault. It does not
authorize arbitrary private or metadata access. The current runtime still reads
`AI_PROVIDER_TRUSTED_ORIGINS` as an exact-origin trust input for intentionally
approved private providers; this input is migration debt, not a feature-enable
step. TS-795 does not remove the validation or change that runtime policy.

Each entry approves exactly its scheme, hostname or IP address, and effective
port. HTTP defaults to port 80 and HTTPS to port 443; explicit default ports are
equivalent. DNS hostnames are case-insensitive and a final DNS dot is normalized.
Approval does not include other hosts, subdomains, schemes, or ports. Use ASCII
DNS names or IP literals, with brackets for IPv6.

Entries must be origins: no wildcard, credentials, path (including a trailing
slash), query, fragment, or scoped IPv6 address. The server rejects invalid
configuration at startup. The workspace provider URL may still include its API
path, such as `http://localhost:11434/v1`; its trusted origin is
`http://localhost:11434`. An empty or absent variable permits public addresses
only. Workspace owners cannot change this operator policy through the API.

Every new connection resolves its destination once. For an untrusted origin,
every returned address must be public; mixed public/private DNS results fail
closed. The server then dials only those checked IP literals, preventing a second
DNS resolution from bypassing the check. Existing connections continue to use
their previously checked destinations. Trusted origins permit intentional
nonpublic addresses while retaining URL validation, normal TLS certificate
verification, and the redirect ban.

AI requests ignore `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY`. An implicit proxy
could otherwise resolve or reach an unchecked private destination. Redirects are
never followed, even between trusted origins. URL credentials, query parameters,
and fragments remain forbidden in workspace provider URLs. Provider credentials
and raw upstream error messages remain excluded from errors returned to callers.

Before rollout, inspect the existing provider URL origins without reading their
keys. Qualify only explicitly approved origins under the current exact-origin trust
policy and replay connection testing in a disposable fixture. An unapproved private provider
receives a controlled server-network-policy error without making an HTTP request.

The test-only live checker creates its own backend and does not inherit this variable
from the operator's shell. To replay an intentional private provider, pass
`--trust-provider-origin` with `--provider-base-url` to
`scripts/check-ai-provider-live.py`. For example, the base URL
`http://localhost:11434/v1` approves only
`http://localhost:11434` for that disposable backend. Without the flag, the
fixture retains the public-only default. The checker validates the URL before
reading credentials, and `--dry-run` shows its exact policy without reading the
projection or starting resources. Its success receipt records the policy and the
SHA-256 of the backend binary actually used; it does not change production policy.
