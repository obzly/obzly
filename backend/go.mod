module obzly

go 1.27.3

require (
	cel.dev/cel-go v0.32.0
	github.com/apache/arrow-go/v18 v18.4.1
	github.com/aws/aws-sdk-go-v2 v1.47.8
	github.com/aws/aws-sdk-go-v2/config v1.33.12
	github.com/aws/aws-sdk-go-v2/credentials v1.20.12
	github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager v0.5.1
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.79.2
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.65.6
	github.com/aws/aws-sdk-go-v2/service/computeoptimizer v1.62.11
	github.com/aws/aws-sdk-go-v2/service/costexplorer v1.74.8
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.343.0
	github.com/aws/aws-sdk-go-v2/service/eks v1.105.1
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.63.6
	github.com/aws/aws-sdk-go-v2/service/iam v1.64.5
	github.com/aws/aws-sdk-go-v2/service/lambda v1.113.4
	github.com/aws/aws-sdk-go-v2/service/organizations v1.62.3
	github.com/aws/aws-sdk-go-v2/service/rds v1.131.2
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.6
	github.com/aws/aws-sdk-go-v2/service/securityhub v1.85.2
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.6
	github.com/aws/smithy-go v1.28.8
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/gin-contrib/cors v1.7.9
	github.com/gin-contrib/gzip v1.2.8
	github.com/gin-gonic/gin v1.12.0
	github.com/go-jose/go-jose/v4 v4.1.6
	github.com/gogo/protobuf v1.3.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/golang/snappy v1.0.0
	github.com/google/uuid v1.6.0
	github.com/ilyakaznacheev/cleanenv v1.5.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/joho/godotenv v1.5.1
	github.com/marcboeker/go-duckdb/v2 v2.4.3
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/nats-io/nats-server/v2 v2.15.2
	github.com/nats-io/nats.go v1.55.0
	github.com/pquerna/otp v1.5.0
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/common v0.73.0
	github.com/prometheus/prometheus v0.315.1
	github.com/shirou/gopsutil/v4 v4.26.9
	github.com/stretchr/testify v1.12.1
	github.com/swaggo/files v1.0.1
	github.com/swaggo/gin-swagger v1.6.1
	github.com/swaggo/swag v1.16.6
	github.com/valkey-io/valkey-go v1.0.79
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.opentelemetry.io/proto/otlp v1.11.0
	gocloud.dev v0.47.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/term v0.47.0
	golang.org/x/time v0.16.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261026165753-2ffab32bce7e
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/postgres v1.6.3
	gorm.io/gorm v1.31.2
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	github.com/KyleBanks/depth v1.2.1 // indirect
	github.com/andybalholm/brotli v1.2.7 // indirect
	github.com/antithesishq/antithesis-sdk-go v0.8.0-default-no-op // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/apache/thrift v0.22.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.25 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.2 // indirect
	github.com/bytedance/sonic/loader v0.5.1 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dennwc/varint v1.0.0 // indirect
	github.com/duckdb/duckdb-go-bindings v0.1.24 // indirect
	github.com/duckdb/duckdb-go-bindings/darwin-amd64 v0.1.24 // indirect
	github.com/duckdb/duckdb-go-bindings/darwin-arm64 v0.1.24 // indirect
	github.com/duckdb/duckdb-go-bindings/linux-amd64 v0.1.24 // indirect
	github.com/duckdb/duckdb-go-bindings/linux-arm64 v0.1.24 // indirect
	github.com/duckdb/duckdb-go-bindings/windows-amd64 v0.1.24 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/edsrzf/mmap-go v1.2.1-0.20241212181136-fad1cd13edbd // indirect
	github.com/facette/natsort v0.0.0-20181210072756-2cd4dd1e2dcb // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.13 // indirect
	github.com/gin-contrib/sse v1.1.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-openapi/jsonpointer v1.0.1 // indirect
	github.com/go-openapi/jsonreference v1.0.1 // indirect
	github.com/go-openapi/spec v1.0.0 // indirect
	github.com/go-openapi/swag/conv v0.29.2 // indirect
	github.com/go-openapi/swag/jsonutils v0.29.2 // indirect
	github.com/go-openapi/swag/loading v0.29.2 // indirect
	github.com/go-openapi/swag/pools v0.29.2 // indirect
	github.com/go-openapi/swag/stringutils v0.29.2 // indirect
	github.com/go-openapi/swag/typeutils v0.29.2 // indirect
	github.com/go-openapi/swag/yamlutils v0.29.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.3 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/flatbuffers v25.2.10+incompatible // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/wire v0.7.0 // indirect
	github.com/googleapis/gax-go/v2 v2.24.1 // indirect
	github.com/grafana/regexp v0.0.0-20250905093917-f7b3be9d1853 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/asmfmt v1.3.2 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/marcboeker/go-duckdb/arrowmapping v0.0.21 // indirect
	github.com/marcboeker/go-duckdb/mapping v0.0.21 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/minio/asm2plan9s v0.0.0-20200509001527-cdd76441f9d8 // indirect
	github.com/minio/c2goasm v0.0.0-20190812172519-36a3d3bbc4f3 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pandatix/go-abnf v0.5.1 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/power-devops/perfstat v0.0.0-20260805114148-88456608a4f6 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.60.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tklauser/go-sysconf v0.4.0 // indirect
	github.com/tklauser/numcpus v0.12.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.mongodb.org/mongo-driver/v2 v2.8.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.29.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20260924152758-ed294f943157 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	google.golang.org/api v0.297.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/apimachinery v0.37.0 // indirect
	k8s.io/client-go v0.37.0 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/utils v0.0.0-20260626114624-be93311217bd // indirect
	olympos.io/encoding/edn v0.0.0-20201019073823-d3554ca0b0a3 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)
