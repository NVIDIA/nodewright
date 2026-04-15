## Getting Started

### Dependencies
The operator handles its own certificate management for webhooks, so no additional dependencies are required.

### Usage Example Custom Resource

Usage once operator is installed looks like this, you can apply skyhook packages. In this example we are applying 2 (nvssh, and bcp).

```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: skyhook-example
spec:
  additionalTolerations:
    - key: nvidia.com/gpu
      operator: Exists
  nodeSelectors:
    matchLabels:
      agentpool: gpu
  podNonInterruptLabels:
    matchLabels:
      key: value
  interruptionBudget: 
    percent: 33
  packages:
    something_important:
      version: 1.0.0
      image: ghcr.io/nvidia/skyhook-packages/shellscript
      dependsOn:
        tuning: 1.0.0
      configMap:
        apply.sh: |-
          #!/bin/bash
          echo "hello world" > /skyhook-hello-world
          sleep 60
        apply_check.sh: |-
          #!/bin/bash
          cat /skyhook-hello-world
          sleep 30
        config.sh: |-
          #!/bin/bash
          echo "a config is run" >> /skyhook-hello-world
          sleep 60
        config_check.sh: |-
          #!/bin/bash
          grep "config" /skyhook-hello-world
          sleep 30
    tuning:
      version: 1.0.0
      image: ghcr.io/nvidia/skyhook-packages/tuning
      interrupt:
          type: reboot
      configInterrupts:
        grub.conf:
          type: reboot
        sysctl.conf:
          type: restart_all_services
      configMap:
        grub.conf: |-
            hugepagesz=1G
            hugepages=2
            hugepagesz=2M
            hugepages=5128
        sysctl.conf: |-
            fs.inotify.max_user_instances=65535
            fs.inotify.max_user_watches=524288
            kernel.threads-max=16512444
            vm.max_map_count=262144
            vm.min_free_kbytes=65536
        ulimit.conf: |-
            memlock: 128
            fsize: 1000
```

Packages can depend on each other, so if you needed `something_important` to be installed after `tuning` you can define that like this:

```yaml
    something_important:
      ...
      dependsOn: 
        tuning: "1.0.0"
    tuning:
      ...
```

## Development

### Prerequisites
- go version v1.26.2+
- docker version 17.03+ or podman 4.9.4+ (project makefile kind of assumes podman)
- kubectl version v1.27.3+.
- Access to a Kubernetes v1.27+ cluster. (We test on 1.32+, could work on older, not regularly tested. Could be api compatibilities issues.)


**Install the CRDs into the cluster:**
```sh
make install
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin 
privileges or be logged in as admin.

**Run the Operator**
```sh
make run ## will run in background process, not in kubernetes
make kill ## kills background process
```

or you can build and run this way
```sh
make build
./bin/manager
```

**Create instances of your solution**
You can apply the [examples](./config/samples/) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**NOTE:** You may need to run [kind in experimental mode](https://kind.sigs.k8s.io/docs/user/rootless/#creating-a-kind-cluster-with-rootless-podman) when using `make` create-kind-cluster. Run `make --help` for more information on all potential `make` targets
```bash
❯ make help

Usage:
  make <target>

General
  help             Display this help.
  clean            Clears out the local build folder

Development
  manifests        Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
  generate         Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
  generate-mocks   Generate code for interface mocking
  license-report   Run run license report
  license-check    Run go-licenses check against code.
  license-fmt      Run add license header to code.
  fmt              Run go fmt against code.
  vet              Run go vet against code.
  test             Run all tests.
  watch-tests      watch unit tests and auto run on changes.
  unit-tests       Run unit tests.
  e2e-tests        Run end to end tests.
  create-kind-cluster  deletes and creates a new kind cluster. versions is set via KIND_VERSION
  podman-create-machine  creates a podman machine
  lint             Run golangci-lint linter & yamllint
  lint-fix         Run golangci-lint linter and perform fixes

Build
  build            Build manager binary.
  run              Run a controller from your host.
  docker-build     Build docker image with the manager.

Deployment
  create-namespace  Create the namespace in the K8s cluster specified in ~/.kube/config.
  install          Install CRDs into the K8s cluster specified in ~/.kube/config.
  install-helm-chart  Install helm chart into the K8s cluster specified in ~/.kube/config.
  uninstall-helm-chart  Uninstall helm chart from the K8s cluster specified in ~/.kube/config.
  uninstall        Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
  deploy           Deploy controller to the K8s cluster specified in ~/.kube/config.
  generate-helm    Generates new helm chart using helmify. Be-careful, this can break things, it overwrites files, make sure to look at you git diff.
  undeploy         Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.

Build Dependencies
  install-deps     Install all dependencies
  golangci-lint    Download golangci locally if necessary. 
  kustomize        Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
  controller-gen   Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
  envtest          Download envtest-setup locally if necessary.
  gocover-cobertura  Download gocover-cobertura locally if necessary.
  ginkgo           Download ginkgo locally if necessary.
  mockery          Download mockery locally if necessary.
  chainsaw         Download chainsaw locally if necessary.
  helm             Download helm locally if necessary.
  helmify          Download helmify locally if necessary.
  go-licenses      Download  go-licenses locally if necessary.
```

# Deployment

The helm repos are currently being migrated. Please use the chart directly in this repo.

Operator containers:
- ghcr.io/nvidia/skyhook/operator

## Deploy from main

If you want to test the helm chart, this is how you can deploy it from the repo.

```
## setup namespace "skyhook"
kubectl create namespace skyhook --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic node-init-secret --from-file=.dockerconfigjson=${HOME}/.config/containers/auth.json --type=kubernetes.io/dockerconfigjson -n  skyhook

## install operator
helm install skyhook-operator ./chart --namespace skyhook
```

to remove operator from a cluster:
```
## remove operator (automatic cleanup enabled by default)
helm uninstall skyhook-operator --namespace skyhook

## delete CRD
make uninstall
```

**NOTE**: The Helm chart includes automatic cleanup of Skyhook and DeploymentPolicy resources during uninstall (enabled by default). If you've disabled automatic cleanup (`cleanup.enabled: false`), you must manually delete SCRs before uninstalling to avoid finalizer issues. If you remove the operator before deleting SCRs with finalizers, they can hang. To fix: reinstall the operator, delete resources, then uninstall properly. Manual cleanup may require removing configmaps, uncordoning nodes, removing taints, and deleting running pods.


## Helm Chart and General Config infra
This repo uses kubebuilder for scaffolding this project, and lots of functions in the makefile are hooked up to this as well. Kubebuilder for the most part manages the [config](./config) directory which uses kustomize heavily. To convert this structure into helm, we using a tool call helmify. Its a generic tool that converts this kubebuilder kustomize into a helm chart. It does not do everything, just a lot of it. So once you call `make generate-helm` you might need to make some additional changes or revert some of its changes. At some point we might want to stop using it if if becomes more work, but for now it does a pretty good job keep the two different ways of managing config in sync. 

Common work flow looks like: change something that requires updating config. First do `make manifest` and `make generate`. This will alter the config based on comments let in code. Example would be in the CRD `//+kubebuilder:validation:Enum=service;reboot`. Changes to the CRD will require those 2 make functions( well depending what you change might need just one). Then to keep in sync do `make generate-helm` which ask `are you sure` this is because it might not do exactly what you think. To keep in sync you need to say Y, and edit the chart as needed or do it by hand. Depending on what you did. Might make sense to do this in a clean git state so you can see what its doing. Mostly making it sound scarier then it is, just be warned.

