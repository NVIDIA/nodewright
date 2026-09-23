## Description
<!-- Provide a brief description of the changes in this PR. -->
<!-- Reference any issues closed by this PR with "closes #1234". -->

## How this was verified
<!-- Which tests and linters did you run, and what did you not run? -->
<!-- Go code changed: `make vet lint unit-tests` from operator/ is the minimum. That is exactly what CI's unit lane runs, and note that `make unit-tests` on its own does NOT run the linter. -->
<!-- Agent changed: `make test lint` from agent/, which is what CI's agent lanes run. -->
<!-- Docs, comments or other non-code changes only: say so, that is a complete answer. -->
<!-- Could not run something (no cluster for e2e, for example)? Say which and why. -->

## Checklist

- [ ] I am familiar with the [Contributing Guidelines](https://github.com/NVIDIA/nodewright/blob/main/CONTRIBUTING.md).
- [ ] My commits are signed off per the [DCO](https://developercertificate.org/) **and** cryptographically signed: `git commit -s -S`.
- [ ] If this changes code, I ran the relevant suite locally and recorded the result above.
- [ ] New or existing tests cover these changes.
- [ ] The documentation is up to date with these changes.
- [ ] If an AI tool wrote a meaningful part of this change, I have said so above, and I can explain and defend every line of it.
