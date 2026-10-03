# Third-party licenses

Go modules compiled into release binaries (`linux`, build tags `nvml,iap`). All
licenses are compatible with Apache-2.0. Builds without tags contain only the
standard library and `go.yaml.in/yaml/v3`.

The NVIDIA Management Library (`libnvidia-ml.so.1`) is **not** distributed: it
is part of the NVIDIA driver and is loaded at runtime if present.

Regenerate with:

```sh
GOOS=linux GOFLAGS=-tags=nvml,iap go-licenses report ./cmd/groundtruth-agent
```

| Module | License |
|---|---|
| `cloud.google.com/go/auth` | Apache-2.0 |
| `cloud.google.com/go/compute/metadata` | Apache-2.0 |
| `github.com/cespare/xxhash/v2` | MIT |
| `github.com/felixge/httpsnoop` | MIT |
| `github.com/go-logr/logr` | Apache-2.0 |
| `github.com/go-logr/stdr` | Apache-2.0 |
| `github.com/google/s2a-go` | Apache-2.0 |
| `github.com/googleapis/enterprise-certificate-proxy/client` | Apache-2.0 |
| `github.com/googleapis/gax-go/v2` | BSD-3-Clause |
| `github.com/NVIDIA/go-nvml/pkg/nvml` | Apache-2.0 |
| `go.opentelemetry.io/auto/sdk` | Apache-2.0 |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | Apache-2.0 / BSD-3-Clause |
| `go.opentelemetry.io/otel/metric` | Apache-2.0 / BSD-3-Clause |
| `go.opentelemetry.io/otel/trace` | Apache-2.0 / BSD-3-Clause |
| `go.opentelemetry.io/otel` | Apache-2.0 / BSD-3-Clause |
| `go.yaml.in/yaml/v3` | MIT / Apache-2.0 |
| `golang.org/x/crypto` | BSD-3-Clause |
| `golang.org/x/net` | BSD-3-Clause |
| `golang.org/x/sys/unix` | BSD-3-Clause |
| `golang.org/x/text` | BSD-3-Clause |
| `google.golang.org/api/googleapi` | BSD-3-Clause |
| `google.golang.org/api/internal/third_party/uritemplates` | BSD-3-Clause |
| `google.golang.org/genproto/googleapis/rpc` | Apache-2.0 |
| `google.golang.org/grpc` | Apache-2.0 |
| `google.golang.org/protobuf` | BSD-3-Clause |
