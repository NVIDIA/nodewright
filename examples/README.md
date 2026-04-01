# Skyhook Examples

This directory contains sample manifests and usage patterns for Skyhook. Use these examples to learn how to configure, test, and extend Skyhook in your own clusters.

## Included Examples

- `debug_pod.yaml`: Example manifest for launching a debug pod in your cluster.
    - Replace the `{{ set hostname }}` with the hostname you want to put the debug pod onto
    - `kubectl apply -f examples/debug_pod.yaml`
    - Once launched do `kubectl exec -ti debug-pod sh -c 'chroot /host bash'`
    - You will then be on the host as root
    - Once done, exit and run `kubectl delete po/debug-pod` to remove the pod
- `simple/`: Minimal working examples of Skyhook Custom Resources and package configurations.
- `interrupt-wait-for-pod/`: Example showing how to use interrupts and wait-for-pod logic with Skyhook.

Feel free to use, modify, or extend these examples for your own use cases! 