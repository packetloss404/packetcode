# Releases

How a packetcode release is built, what is signed, and how anyone can check it.

## Cutting a release

First wait for CI on the exact commit to finish successfully, including the
cross-platform tests, lint, vulnerabilities, executable smoke, browser manual,
PTY goldens, six builds, and release dry run. Then push a new version tag.
`.github/workflows/release.yml` does the rest. Packetcode has no hosted web
deployment in this repository; publishing a release is its distribution step.

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Replace `vX.Y.Z` with an unused version. Preserve a failed tag as evidence and
use a new patch version for a source fix; do not move a published tag. The
`v0.6.1` preflight caught a Windows concurrent-save failure before publication,
so those changes and the fix continue in `v0.6.2`.

The workflow runs the test suite on Linux, macOS and Windows, the TUI golden
check, and the Chromium manual check before publication, so a tag that fails
those gates publishes nothing. After it completes, check the release assets
and the signing/provenance steps; a successful main-branch CI run does not
establish that a later publication succeeded.

Nothing about the pipeline waits for a tag to be exercised. CI builds a full
snapshot on every push (`make release-dry-run`) and asserts the result with
`scripts/check-release-artifacts.sh` — six archives, checksums that match, the
binary and `LICENSE` inside each one, and the built Linux binary actually
running. `goreleaser check` alone validates the config file and none of that,
and the difference between the two is where a release breaks on the day it is
most expensive to find out.

## What is published

| File | What it is |
| --- | --- |
| `packetcode-{linux,darwin,windows}-{amd64,arm64}.{tar.gz,zip}` | the binary, plus `LICENSE`, `README.md` and `CHANGELOG.md` |
| `checksums.txt` | SHA-256 of every archive |
| `checksums.txt.sig`, `checksums.txt.pem` | Sigstore signature over `checksums.txt`, and the certificate it was made with |

Builds are reproducible: `-trimpath`, `CGO_ENABLED=0`, and `mod_timestamp` taken
from the commit rather than the clock, so two builds of one tag are
byte-identical and "verify it yourself" is something a person can actually do.

## Verifying a download

The installers do this for you. `install.sh` verifies the signature when
`cosign` is on `PATH` and says plainly when it is not; `install.ps1` takes
`-RequireSignature`. Both refuse outright when a signature is *present and
invalid* — that case is a failed guarantee, never a missing one, and is never
softened into the "unsigned release" path.

By hand:

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github.com/packetloss404/packetcode/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```

Both steps matter, and neither substitutes for the other. `sha256sum` alone
proves the archive matches `checksums.txt` — but whoever could serve a modified
archive could serve the matching `checksums.txt` beside it, and the check would
pass on the forgery. Verifying the signature is what makes the chain end
somewhere an attacker does not control.

Build provenance is attested separately, and answers a different question — not
"is this file ours" but "how was it made":

```bash
gh attestation verify packetcode-linux-amd64.tar.gz --repo packetloss404/packetcode
```

## Signing

Three signatures, because they fail separately and buy different things.

| | Purpose | Needs |
| --- | --- | --- |
| Sigstore over `checksums.txt` | the checksum file is the one our workflow produced | nothing — keyless, via the workflow's OIDC token |
| Apple Developer ID + notarization | macOS runs it without a Gatekeeper block | a paid Apple Developer account |
| Authenticode | Windows SmartScreen stops warning | a paid code-signing certificate |

**Sigstore signing is already on.** It needs no key, no secret and no renewal:
`cosign` exchanges the workflow's GitHub OIDC token for a certificate that lives
for minutes, and the exchange is recorded in a public transparency log. The
release job grants `id-token: write`, so it always has a token — and a missing
`checksums.txt.sig` fails the release rather than shipping quietly unsigned.

**The two OS signatures are wired but dormant**, because both require
certificates that must be bought. Until then a release still completes, and says
so in the job summary rather than leaving it to be inferred from which steps
were quiet.

### Turning on macOS signing and notarization

Handled by GoReleaser on the Linux runner — no macOS machine is involved. You
need an Apple Developer account (99 USD/year) for both halves.

1. Create a **Developer ID Application** certificate and export it as `.p12`.
2. Create an **App Store Connect API key** with the Developer role
   (App Store Connect → Users and Access → Integrations → Keys).
3. Add these repository secrets:

   | Secret | Value |
   | --- | --- |
   | `MACOS_SIGN_P12` | `base64 -i cert.p12` |
   | `MACOS_SIGN_PASSWORD` | the `.p12` passphrase |
   | `MACOS_NOTARY_ISSUER_ID` | the API key's issuer UUID |
   | `MACOS_NOTARY_KEY_ID` | the API key ID |
   | `MACOS_NOTARY_KEY` | contents of the `.p8` private key |

Setting `MACOS_SIGN_P12` is what enables the step. The release waits for Apple's
verdict rather than firing and forgetting — an unwaited submission means the
release publishes before anyone knows whether it passes Gatekeeper, and the
first report would be a user who cannot open it.

### Turning on Windows signing

`scripts/sign-windows.sh` runs as a post-build hook and uses `osslsigncode`,
which the release job installs unconditionally — installing it only when the
secret exists would mean the first release that uses the certificate is also the
first to exercise the install step.

| Secret / variable | Value |
| --- | --- |
| `WINDOWS_SIGN_P12` (secret) | `base64 -w0 cert.p12` |
| `WINDOWS_SIGN_PASSWORD` (secret) | its passphrase |
| `WINDOWS_SIGN_TIMESTAMP` (variable, optional) | RFC3161 server; defaults to DigiCert's |

Signatures are always timestamped. An untimestamped one stops validating the day
the certificate expires, which retroactively turns every past release into a
warning.

Note that SmartScreen reputation accrues to the certificate, so early signed
releases may still warn. An OV certificate builds reputation over time; an EV
certificate starts with it.

### `REQUIRE_SIGNING`

Set the repository **variable** `REQUIRE_SIGNING` to `1` once the certificates
exist. A missing certificate then fails the release instead of producing an
unsigned download that looks exactly like a signed one until somebody tries to
verify it.

Leave it unset before then, or every release fails for a reason you already
know about.

## Local checks

```bash
make release-dry-run   # build every artifact and assert it
make release-check     # assert an existing dist/ without rebuilding
make install-test      # the installers' signature logic, against stubs
```

`make release-dry-run` takes the same skip paths a fork and a pull request take,
since none of them have signing credentials — which is worth exercising, because
it is the path most runs of this pipeline follow.
