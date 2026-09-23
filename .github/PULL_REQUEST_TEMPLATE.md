## Description
<!-- Provide a brief description of the changes in this PR. -->
<!-- Reference any issues closed by this PR with "closes #1234". -->

## Testing
<!-- What ran, and where. CI runs on kind, whose nodes are containers: it cannot exercise a real reboot, systemd shutdown, kernel modules or drivers, GPU workloads, or drains of real workloads. If your change touches interrupts, signal handling, cordon/drain, `on_host` step execution, or anything else that mutates the host, say whether it ran on a real node. -->

- [ ] Unit tests
- [ ] e2e on kind (CI or local)
- [ ] Real node: <!-- OS, hardware where it matters, and what was exercised -->

**Not exercised:** <!-- What this change affects that none of the above ran, and why. "Nothing" is a valid answer; leaving this blank is not. -->

## Checklist

- [ ] I am familiar with the [Contributing Guidelines](https://github.com/NVIDIA/nodewright/blob/main/CONTRIBUTING.md).
- [ ] My commits are signed off per the [DCO](https://developercertificate.org/) **and** cryptographically signed: `git commit -s -S`.
- [ ] New or existing tests cover these changes.
- [ ] The documentation is up to date with these changes.
