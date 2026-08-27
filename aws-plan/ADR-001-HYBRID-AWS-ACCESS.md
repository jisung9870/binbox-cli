# ADR-001. AWS resource 조회는 SDK, profile과 인증 연결은 CLI를 사용한다

독자        저장소 소유자와 구현·검토 담당자
목적        AWS Browse v2의 AWS 접근 방식과 검증 gate를 고정한다
대상 환경   Go 1.25, AWS SDK for Go v2, AWS CLI v2, 여러 AWS profile
결정일      2026-08-27
상태        Accepted, 구현·자동 Linux PTY·자동 release gate 완료; real AWS/CloudTrail 외부 gate 대기

관련 문서   [PRD](PRD.md) · [설계](DESIGN.md) · [동작 시나리오](SCENARIOS.md) · [아키텍처](ARCHITECTURE.md) · [구현 작업 방식](IMPLEMENTATION-WORKFLOW.md) · [검토 기록](REVIEW.md)

## Context

기존 기획은 STS, EC2, IAM, Route 53을 AWS CLI subprocess로 호출하고 JSON을 decode하는 방식을 선택했다. 이 방식은 기존 `bb assume`과 AWS CLI profile 해석을 재사용하지만, cross-profile 검색에서 profile마다 여러 process를 반복 실행한다.

2026-08-27 개발 환경에서 network-free AWS CLI capability command 10회를 실행한 결과는 4.47초였다. 호출당 평균 약 447ms이며 API network latency를 포함하지 않는다. 이 수치는 운영 성능 측정값이 아니라 process 시작·설정 해석 비용이 fan-out에 누적된다는 로컬 지표다.

AWS Browse v2는 다음 기능을 P0부터 요구한다.

- profile 여러 개의 제한된 동시 검색과 streaming result.
- route·overlay 단위 취소와 timeout.
- page 단위 progressive loading.
- 서비스·operation·error code를 보존한 오류 분류.
- connection과 credential cache 재사용.

AWS SDK for Go v2는 shared profile, SSO, assume-role, process credentials를 지원하며, SDK request는 `context.Context`, standard retry, typed API error를 제공한다. SSO token이 만료되면 사용자는 AWS CLI login을 다시 실행해야 한다. 근거: [SDK configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html), [retries and timeouts](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-retries-timeouts.html), [error handling](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/handle-errors.html).

## Decision

AWS Browse v2는 hybrid access model을 사용한다.

### AWS CLI가 맡는 control plane

- `aws configure list-profiles`: browser 검색 scope discovery.
- `aws sso login`, profile 기반 `bb profile login <profile>`, session 기반 `bb aws sso <session>`: interactive authentication.
- 기존 `bb assume`과 `bb aws assume`: released shell credential workflow.
- ambient/named credential bridge: `aws configure export-credentials [--profile <NAME>] --format process`.

CLI는 AWS resource data-plane operation을 실행하지 않는다. EC2, IAM, Route 53, STS 조회의 CLI fallback도 만들지 않는다.

### AWS SDK for Go v2가 맡는 data plane

- `config`, `sts`, `ec2`, `iam`, `route53` module을 사용한다.
- context별 SDK config와 service client를 한 번 만들고 session 동안 재사용한다.
- 모든 AWS resource operation은 SDK request로 실행한다.
- progressive browse는 SDK paginator 또는 명시적 cursor input을 사용한다.
- coordinator cancel은 request의 `context.Context`를 취소한다.
- 오류는 modeled error, `smithy.OperationError`, `smithy.APIError` 순서로 분류한다.

### Credential isolation

ambient와 named context 모두 같은 credential bridge를 사용한다. Ambient는 profile 인자 없이 `aws configure export-credentials --format process`를 실행해 현재 environment, `AWS_PROFILE`, `bb assume` credential을 AWS CLI의 현재 해석대로 받는다. Named는 `--profile <NAME>`을 추가하고 parent identity environment를 제거한다. SDK default credential chain은 사용하지 않는다. 이 선택은 SDK config load 중 SSO/STS가 별도 endpoint로 나가는 경로와 ambient/named test seam의 차이를 없앤다.

named profile은 `WithSharedConfigProfile`로 credential을 선택하지 않는다. AWS SDK 공식 계약상 environment credential이 explicit shared profile보다 우선하므로, bb는 direct argv credential bridge를 별도 `aws.CredentialsProvider`로 구현한다. Provider는 identity environment를 제거한 child에서 `aws configure export-credentials`를 실행하고 process JSON을 memory에서만 decode한다. Raw provider를 `config.WithCredentialsProvider`로 넘기면 `LoadDefaultConfig`가 `CredentialsCache`로 한 번만 감싸며, `config.WithCredentialsCacheOptions`로 expiry window와 jitter를 설정한다. 한 context는 `LoadDefaultConfig`를 한 번만 실행하고 STS·EC2·IAM·Route 53 client가 같은 config/cache를 재사용한다. 수동 이중 wrapping은 금지한다. 근거: [SDK profile and environment precedence](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html), [AWS CLI credential export](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html).

named profile의 `credential_source=Environment`는 P0 cross-profile scope에서 `Unsupported credential source`다. ambient current context에서만 지원한다. credential, policy document, tag value, raw payload는 log와 disk cache에 기록하지 않는다.

### Endpoint trust

현재 SDK public `LoadOptions`에는 configured endpoint를 끄는 field나 helper가 없다. 따라서 `LoadDefaultConfig` 후 `cfg.BaseEndpoint=nil`로 지우고, `GetIgnoreConfiguredEndpoints(context.Context) (true, true, nil)`을 구현한 bb config source를 `cfg.ConfigSources` 맨 앞에 넣는다. 각 service client는 `Options.BaseEndpoint=nil`과 기본 `EndpointResolverV2`만 사용한다. Process-global `os.Setenv`는 병렬 context와 test를 오염시키므로 금지한다. Credential child는 `AWS_ENDPOINT_URL`, `AWS_ENDPOINT_URL_*`를 제거하고 `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true`를 설정한다. 이 public API 경로는 SDK version을 고정한 compile test와 global env, service env, profile `endpoint_url`/`services` poison-listener test가 모두 통과해야 채택된다. P0는 custom endpoint를 지원하지 않는다. 근거: [SDK config sources](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws#Config), [configured endpoint controls](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html), [Go v2 endpoint configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html).

`credential_process`는 사용자가 profile에 등록한 외부 실행 파일이다. bb는 그 process가 내부에서 수행하는 network access까지 통제하지 않으며, 이를 credential trust boundary로 명시한다.

### Read-only boundary

각 provider는 좁은 SDK client interface만 받는다. 예를 들어 EC2 provider는 `DescribeInstances`, `DescribeVolumes`, `DescribeSecurityGroups`, `DescribeSecurityGroupRules`, `DescribeVpcs`, `DescribeSubnets`, `DescribeRouteTables`만 볼 수 있다. Concrete SDK client는 factory package 밖으로 노출하지 않는다.

새 read operation은 다음 네 항목을 함께 추가해야 한다.

1. provider interface method.
2. 최소 IAM permission 문서.
3. fake client fixture와 call assertion.
4. 실계정 CloudTrail read-only smoke.

## Consequences

### 얻는 것

- resource API마다 process를 시작하지 않는다.
- SDK client와 HTTP transport를 context별로 재사용한다.
- API page, error code, request ID를 typed data로 다룬다.
- cancellation과 timeout이 TUI route lifecycle과 직접 연결된다.
- CLI version별 JSON output 차이가 resource model에 전파되지 않는다.

### 부담하는 비용

- `go.mod`와 binary에 AWS SDK config·STS·EC2·IAM·Route 53 module이 추가된다.
- SDK update와 generated API model 변경을 유지보수해야 한다.
- named profile credential bridge와 expiration refresh를 별도로 test해야 한다.
- SDK가 읽는 endpoint·retry·credential 설정을 P0 security contract에 맞춰 명시적으로 제한해야 한다.

## Alternatives considered

### 모든 조회를 AWS CLI로 유지

새 dependency가 없고 credential 동작이 AWS CLI와 일치한다. 반복 process 비용, stderr error parsing, manual pagination, child cancellation 복잡도가 cross-profile 기능에 누적되므로 채택하지 않는다.

### AWS CLI를 완전히 제거하고 SDK만 사용

resource 조회는 단순해지지만 profile discovery, SSO login, released `bb assume`과 중복되는 인증 UX를 다시 만들어야 한다. named profile과 ambient environment precedence도 별도 해결이 필요해 채택하지 않는다.

### CLI와 SDK data-plane fallback을 함께 유지

장애 우회 경로를 제공하지만 operation별 구현·fixture·오류 의미가 두 벌이 된다. P0는 한 data-plane만 유지한다.

## P0 validation gate

구현 Phase 1 전에 credential·security spike가 다음 항목을 통과해야 한다.

1. ambient raw environment와 `bb assume` credential로 SDK STS identity가 일치한다.
2. static, SSO cache, `role_arn/source_profile`, `credential_process` named profile이 credential bridge를 거쳐 올바른 account를 반환한다.
3. expired SSO는 `login required`, named `credential_source=Environment`는 `Unsupported`로 분류한다.
4. 같은 account를 보는 profile 두 개의 credential과 observation을 섞지 않는다.
5. environment/profile custom endpoint가 SDK resource request와 credential child에 적용되지 않는다.
6. search cancel이 SDK request context와 실행 중 credential child를 종료한다.
7. cross-profile resource operation의 AWS CLI subprocess 수는 0이며 named profile credential export는 expiration당 최대 1회다.
8. SDK 도입 전후 binary size와 12-profile exact role/domain time-to-first-result를 같은 fixture에서 측정해 기록한다.
9. cache상 유효한 credential로 SDK가 `ExpiredToken`을 반환하면 cache를 invalidate하고 read operation을 한 번만 재시도한다.
10. credential refresh generation이 바뀌면 STS identity를 다시 검증하며 account/partition이 달라진 response는 이전 context key에 commit하지 않는다.
11. `go build -trimpath -ldflags='-s -w' ./cmd/bb` 산출물은 40 MiB 이하다. 2026-08-27 baseline은 5,361,794 bytes다.

gate가 실패하면 resource 조회를 CLI로 되돌리지 않는다. 실패 원인이 SDK config 격리라면 profile별 SDK helper process를 대안으로 검토하고 이 ADR을 supersede하는 새 ADR을 작성한다.
