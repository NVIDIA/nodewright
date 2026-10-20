module github.com/NVIDIA/nodewright/agent

go 1.27.1

require (
	github.com/NVIDIA/nodewright/artifact v0.0.0-00010101000000-000000000000
	github.com/onsi/ginkgo/v2 v2.33.0
	github.com/onsi/gomega v1.44.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/stretchr/testify v1.12.1
	go.yaml.in/yaml/v3 v3.0.5
	oras.land/oras-go/v2 v2.6.2
)

require (
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20260402051712-545e8a4df936 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	golang.org/x/mod v0.36.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/tools v0.45.0 // indirect
)

replace github.com/NVIDIA/nodewright/artifact => ../artifact
