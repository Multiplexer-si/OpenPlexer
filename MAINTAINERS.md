# Maintainers

## Review and merge a pull request

1. Assign one owner to the pull request. The owner describes the change and records the checks they ran.
2. Have a reviewer other than the owner check the diff and verify the affected behavior. Record the result in the pull request.
3. Wait for the CI `check` job to pass. If the pull request touches `spec/`, confirm that the extractor's `check` command reported zero problems. [Extractor check](CONTRIBUTING.md#regenerate-the-contracts-and-fixtures).
4. The maintainers merge the pull request into `main` after those checks pass and review findings are resolved.

## Cut a release

To cut a release, follow the [release guide](docs/release.md).
Include [LICENSE](LICENSE), [NOTICE](NOTICE), and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) with the binaries.

## Triage a security report

1. Reply in the private GitHub report and ask for any missing details. [Security policy](SECURITY.md).
2. Check the report against the trust model and try to reproduce it on the latest release. Record the affected versions and impact.
3. Keep the report and fix private while the maintainers work with the reporter on the timing of the release and advisory.
4. In the advisory, describe the impact and tell users how to upgrade or reduce their exposure.

## Settle a trust decision

Use the issue named in the trust model. Open an issue for a decision that
has none. Describe the proposed behavior, the evidence for it, and its effect
on trust and compatibility. [Open decisions](docs/trust-model.md#open-decisions).

The maintainers decide after review. Record the decision and its reason in
the issue and in the trust model. Update the affected contracts and fixtures
when the decision changes a contract.
