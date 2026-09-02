# RPC validation

Right now, we have validators of proto fields sprawling in multiple places. This README describes
a system where all of the validation are annotated on the protos themselves, and validation happens
in a single place in the frontend.

The following describes a validation system where we can opt-in to semantically validate:
- A request proto, which will drop the request with an error if validation fails.
- A response proto, which will log a warning on the server if validation fails.

## Author workflow

1. Enable request validation, response validation, or both on the RPC (opt-in, to start).
2. Classify every field in each enrolled message with a validation rule.
3. Reuse shared static and dynamic rules. Add a rule only when none has the same meaning.
4. Generate `api-go`, bump server, etc... (same workflow as usual)
5. Potentially write a dynamic validator on the server side (if a new one is added).

To document why a side is not enrolled (i.e., for new RPC request/response), optionally set its `ignored` reason:

```protobuf
option (temporalvalidate.v1.request_validation).ignored =
    "Request validation remains in the existing handler.";
```

Use `response_validation.ignored` to document a response exclusion. Neither
exclusion is required. If you enable a side, every field in that message needs
a validation rule or an explicit coverage exclusion.

### Choose the field rule

| Requirement | Use |
| --- | --- |
| Value-only constraint | `buf.validate.field` |
| Shared Temporal string constraint | A predefined string rule in [rules.proto](v1/rules.proto) |
| Dynamic config limit | A typed `temporalvalidate.v1.dynamic_*` field option |
| Nesting validators (i.e., for a field that is another proto) | `validate_nested = true` or `nested_coverage_ignored` |
| Ignoring a field | `field_coverage_ignored` |

A message field for an opt-in request/response needs an explicit nested decision (including `ignored`). `validate_nested` audits child fields; it does not require the message to be present -- this is on the parent. Use `(buf.validate.field).required = true` when absence of a proto field is invalid.

### Canonical shared rules

Mark a predefined string rule in [rules.proto](v1/rules.proto) with
`canonical_rule`. Its `field_names` list defines which string fields must use it
in enrolled requests and responses. The current canonical fields are `namespace`
and `operation_id`. Additional names can be listed as aliases.

```protobuf
extend buf.validate.StringRules {
  // Must be non-empty.
  optional bool namespace = 10003 [
    (canonical_rule) = {field_names: "namespace"},
    (buf.validate.predefined).cel = {
      id: "temporalvalidate.string.namespace"
      message: "namespace is required"
      expression: "!rule || this.size() > 0"
    }
  ];
}
```

For a matching field, enable the shared rule rather than replacing it with
inline `min_len`, `required`, or custom CEL. You can add constraints alongside it:

```protobuf
string namespace = 1 [
  (buf.validate.field).string.(temporalvalidate.v1.namespace) = true,
  (buf.validate.field).string.max_len = 255
];
```

For lists, apply it through `repeated.items.string`; for string-valued maps,
use `map.values.string`. The field name selects the rule for each value, not the
map keys. Ignore settings must not bypass the shared check. This does not make
an optional field required or require a collection to be nonempty.

Unenrolled sides do not need canonical rules. `field_coverage_ignored` with a
specific reason delegates the whole field, including canonical coverage.
Rules without canonical metadata remain optional to reuse. Canonical declarations
must be singular bool extensions of `buf.validate.StringRules` with an effective
predefined check. Empty names, invalid names, duplicate aliases, and conflicting
canonical declarations fail lint. This metadata affects API lint only; it needs
no Server callback.

### Complete schema example

This shows all of the cases mentioned above inline in an example proto:
It [compiles and is here in this repo](examples/v1/example.proto).

```protobuf
syntax = "proto3";

package temporalvalidate.examples.v1;

import "buf/validate/validate.proto";
import "google/protobuf/duration.proto";
import "temporal/api/common/v1/message.proto";
import "temporalvalidate/v1/rules.proto";

option go_package = "go.temporal.io/api/temporalvalidate/examples/v1;examples";

service ExampleService {
  rpc StartExample(StartExampleRequest) returns (StartExampleResponse) {
    // This only opts-in validation for the request. If validation fails, server errors.
    option (temporalvalidate.v1.request_validation).enabled = true;

    // This only opts-in validation for the response. If validation fails, we log a warning.
    // The intent is that outbound failures IMO happens due to forgetting to set fields rather
    // than inbound where client can send _anything_.
    option (temporalvalidate.v1.response_validation).enabled = true;
  }
}

message StartExampleRequest {
  // Example of a validator that enforces multiple fields at once.
  option (buf.validate.message).cel = {
    id: "example.run_selection"
    message: "run_id is required when use_latest is false"
    expression: "this.use_latest || this.run_id != ''"
  };

  // Require a nonempty namespace. Dynamic rules use this root value.
  string namespace = 1 [(buf.validate.field).string.(temporalvalidate.v1.namespace) = true];
  string operation_id = 2 [
    // Require a nonempty operation ID.
    (buf.validate.field).string.(temporalvalidate.v1.operation_id) = true,
    // Select Server's global ID-length validator.
    (temporalvalidate.v1.dynamic_global_max_id_length) = true
  ];
  // Select the same Server validator. Its length-only check accepts an empty string.
  // This annotation does not generate an ID.
  string request_id = 3 [(temporalvalidate.v1.dynamic_global_max_id_length) = true];
  string run_id = 4 [
    // Skip the UUID check for an empty value. Message CEL still applies.
    (buf.validate.field).ignore = IGNORE_IF_ZERO_VALUE,
    // Require a UUID when the value is nonempty.
    (buf.validate.field).string.uuid = true
  ];
  // Both values are valid. This audit exclusion does not disable message CEL.
  bool use_latest = 5 [(temporalvalidate.v1.field_coverage_ignored) = "both values are valid; run selection is checked by message CEL"];
  Details details = 6 [
    // Require the message to be present.
    (buf.validate.field).required = true,
    // Audit each child field for a validation decision.
    (temporalvalidate.v1.validate_nested) = true
  ];
  repeated Details items = 7 [
    // Allow an empty list, but no more than 10 elements.
    (buf.validate.field).repeated.max_items = 10,
    // Audit the child fields of each element's message type.
    (temporalvalidate.v1.validate_nested) = true
  ];
  map<string, Details> named_items = 8 [
    // Allow an empty map, but no more than 10 entries.
    (buf.validate.field).map.max_pairs = 10,
    // Require each key to be nonempty.
    (buf.validate.field).map.keys.string.min_len = 1,
    // Audit the child fields of the map's value message type.
    (temporalvalidate.v1.validate_nested) = true
  ];
  temporal.api.common.v1.Payload input = 9 [
    // Select Server's namespace payload-size validator.
    (temporalvalidate.v1.dynamic_namespace_max_payload_size) = true,
    // Do not audit opaque child fields. The whole-payload limit still applies.
    (temporalvalidate.v1.nested_coverage_ignored) = "opaque payload; the dynamic rule limits serialized size"
  ];
  // Delegate validation and timeout caps to the named imperative validator.
  // The author must implement that validator; this annotation performs no check.
  google.protobuf.Duration timeout = 10 [(temporalvalidate.v1.field_coverage_ignored) = "Server validateAndNormalizeStartExample validates and caps the timeout"];
}

message Details {
  // Require a nonempty label.
  string label = 1 [(buf.validate.field).string.min_len = 1];
  // Server compares reason's byte length with the limit for StartExampleRequest.namespace.
  // That namespace applies in details, items, named_items, and the response.
  // This length-only check allows an empty reason.
  string reason = 2 [(temporalvalidate.v1.dynamic_namespace_max_reason_length) = true];
}

message StartExampleResponse {
  string operation_id = 1 [
    // Require a nonempty ID in the response.
    (buf.validate.field).string.(temporalvalidate.v1.operation_id) = true,
    // Select the same global ID-length validator used by the request.
    (temporalvalidate.v1.dynamic_global_max_id_length) = true
  ];
  temporal.api.common.v1.Payload result = 2 [
    // If present, check size with config for StartExampleRequest.namespace.
    (temporalvalidate.v1.dynamic_namespace_max_payload_size) = true,
    // Check the whole payload, not its opaque child fields.
    (temporalvalidate.v1.nested_coverage_ignored) = "opaque payload; the dynamic rule limits serialized size"
  ];
  // The message is optional. Audit its child fields and check them when present.
  Details details = 3 [(temporalvalidate.v1.validate_nested) = true];
}
```

### Dynamic option declarations in API

The example imports [temporalvalidate/v1/rules.proto](v1/rules.proto).
These are its three dynamic option declarations; other options are omitted:

```protobuf
extend google.protobuf.FieldOptions {
  optional bool dynamic_global_max_id_length = 900002 [(rule_spec) = {
    scope: DYNAMIC_RULE_SCOPE_GLOBAL
    field_type: TYPE_STRING
  }];
  optional bool dynamic_namespace_max_reason_length = 900005 [(rule_spec) = {
    scope: DYNAMIC_RULE_SCOPE_NAMESPACE
    field_type: TYPE_STRING
  }];
  optional bool dynamic_namespace_max_payload_size = 900006 [(rule_spec) = {
    scope: DYNAMIC_RULE_SCOPE_NAMESPACE
    field_type: TYPE_MESSAGE
    message_type: "temporal.api.common.v1.Payload"
  }];
}
```

Setting one of these options to `true` selects a Server validator.
`rule_spec` declares the validator's scope and supported field type. It does
not define a limit, read config, or implement a check. Unlike the static rules
in the request, these declarations contain no validation expression.

### Matching validators in Server

API-Go generates extension symbols in
[temporalvalidate/v1/rules.pb.go][api-go-rules]. Server's shared
[ValidationRulesProvider][server-rules] binds each option once:

```go
return []dynamicvalidate.Binding{
    {
        Option: temporalvalidatepb.E_DynamicGlobalMaxIdLength,
        Rule: dynamicvalidate.GlobalByteLengthRule(
            dynamicconfig.MaxIDLengthLimit.Get(dc)),
    },
    {
        Option: temporalvalidatepb.E_DynamicNamespaceMaxReasonLength,
        Rule: dynamicvalidate.NamespaceByteLengthRule(
            chasmnexus.MaxReasonLength.Get(dc)),
    },
    {
        Option: temporalvalidatepb.E_DynamicNamespaceMaxPayloadSize,
        Rule: dynamicvalidate.NamespaceMessageSizeRule(
            &commonpb.Payload{}, dynamicconfig.BlobSizeLimitError.Get(dc),
            func(value *commonpb.Payload) int { return value.Size() }),
    },
}
```

This excerpt uses [shared limit helpers][server-rule-helpers] and existing settings.
The imports are `go.temporal.io/api/temporalvalidate/v1` for `temporalvalidatepb`,
`go.temporal.io/api/common/v1` for `commonpb`, and
`go.temporal.io/server/chasm/lib/nexusoperation` for `chasmnexus`.
The binding determines the check:

- `GlobalByteLengthRule` compares string byte length with the current global limit.
  Request `operation_id` and `request_id`, and response `operation_id`, use it.
- `NamespaceByteLengthRule` compares `reason` byte length with the request namespace's
  limit, whether `Details` occurs in the request or response.
- `NamespaceMessageSizeRule` uses the supplied size function. This binding compares
  a present `input` or `result`'s serialized protobuf size, including metadata,
  with the namespace limit.

These bindings already exist. A new RPC that uses them needs no Server validation
code. Do not add a duplicate binding. One option has the same check and setting
wherever it is used. Define a different option for different semantics.
The [integration steps](#implement-a-dynamic-rule) apply only to new dynamic rules.

Namespace-scoped dynamic rules use the request's singular string `namespace`,
including for response fields and children inside lists and maps. Server captures
the namespace before the handler runs; responses need no namespace field.
If the request has no such field, API lint and Server startup reject these rules.
There is no owner-provided resolver or fallback to a response namespace field.
Global dynamic rules and static rules do not need a namespace.

If the request namespace is empty at runtime, response namespace checks report
a context failure and preserve the response. Request enrollment is independent;
response-only enrollment does not add request constraints.
Static CEL checks only values. It does not read config or Server state.
Rules on a shared message type apply wherever that type is validated. Use
separate message types when request and response constraints differ.

The ID-length implementation does not require a nonempty value. The separate
static rule makes `operation_id` required; `request_id` has no such rule.
If the handler must generate a missing request ID, the author must implement
that separately. Neither the option declaration nor the length validator generates it.

`IGNORE_IF_ZERO_VALUE` allows an empty `run_id`; the message CEL requires it
when `use_latest` is false.

The timeout exclusion does not remove timeout validation. The named imperative
validator must validate and normalize it.

## Server integration

### Automatic enrollment

The [shared Registry][server-registry] reads request and response opt-ins from
API-Go's service descriptors. It discovers request and response types without
per-RPC registration. Static-only RPCs need no dynamic binding. At startup,
Server rejects:

- Static rules or CEL that cannot compile.
- A dynamic rule with no implementation or the wrong field type.
- A namespace rule without a singular string namespace in the request.
- Duplicate dynamic implementations.
- Enrollment on a streaming RPC.
- Shared service wrappers that are stale.

The shared interceptor applies validation to enrolled unary RPCs. The HTTP API
uses the same interceptor chain. [Centrally generated service wrappers][server-boundaries]
also protect direct calls through the supplied Workflow and Operator service
interfaces. An interceptor and wrapper on the same invocation check it once.
Unenrolled RPCs pass through unchanged.

Authors do not write `coverage.go`, `ValidationRegistrations`, or a per-owner
`newDynamicValidator`. A new dynamic rule needs one shared binding, not one
binding per RPC. The frontend module already supplies the shared rules.

Adding annotations to an existing RPC does not require wrapper generation.
When API-Go adds RPC methods, regenerate the shared wrappers from the Server
repository root:

```console
go generate ./common/validation
make check-request-validation
```

The generator reads all public API service descriptors. It does not need a
handler source path, interface name, or per-package directive. Its direct
equivalent, also from the Server repository root, is:

```console
go run ./cmd/tools/genrequestvalidation -out common/validation/services_gen.go
```

Do not call raw implementation handlers to bypass the supplied service interfaces.
Raw concrete handlers and internal sub-handlers are not validation boundaries.

The request path is static rules, dynamic rules, imperative validation and
normalization, then request processing. After a successful handler call, enrolled
responses run static and dynamic checks. They do not modify values or return
a new client error. Keep imperative checks during migration.
Compare both paths before removing a check. In particular, preserve defaults,
enum fallback behavior, timeout caps, and payload accounting.

### Response warnings

The [Server response checker][server-responses] uses `logger.Warn`, not
`softassert`, for invalid responses. Each RPC's warning logger allows one warning
per second with a burst of two. A warning includes the RPC, failure type,
implementation or context failure cause, violation count, and up to 10 field
paths and rule IDs.
Collection indices and map keys are replaced with `[*]`. Logs omit payloads,
raw field values, validator error messages, and panic values.

The `response_validation_failures` counter records every failed response, even
when its warning is throttled. Its tags are the RPC (`operation`) and
`validation_failure_type`: `violation`, `namespace_context`, or `implementation`.
Missing or wrong output types and validator panics report implementation failures.
The wrapper still returns the handler's original response and error. Reporting
validates a copy so dynamic message callbacks cannot change the returned values.
It does not make an invalid or unserializable response safe for the client.

Tests should assert both the returned result and the warning or counter.
Keep hard checks for authorization, redaction, and other safety requirements.
Copying and checking responses consumes memory and CPU and adds latency;
warning-only is not a bypass.

### Implement a dynamic rule

The example uses three options that already have shared Server bindings.
There is no Server setup for another RPC that uses these options.
The example service itself is not exposed by Server.

For a new option, declare scope/type metadata in API and generate API-Go.
Then add its check to the shared [ValidationRulesProvider][server-rules].

#### Select the setting and check

Reuse an existing setting with the same meaning. If none exists, declare one
in the appropriate settings package. The existing [reason limit setting][server-setting]
is a namespace-scoped integer with a default of 1000 bytes:

```go
var MaxReasonLength = dynamicconfig.NewNamespaceIntSetting(
    "nexusoperation.limit.reasonLength", 1000,
    "Maximum byte length of a Nexus operation reason.",
)
```

Use its getter directly in the binding:

```go
{
    Option: temporalvalidatepb.E_DynamicNamespaceMaxReasonLength,
    Rule: dynamicvalidate.NamespaceByteLengthRule(chasmnexus.MaxReasonLength.Get(dc)),
},
```

No owning `Config` field or registration function is required for validation.
Pass getters, not values read at startup, so config changes apply to later calls.

Use the shared byte-length and message-size helpers for standard limits.
For a custom check, bind a callback with the [typed constructors][server-dynamic]:

```go
{
    Option: temporalvalidatepb.E_DynamicNamespaceMaxReasonLength,
    Rule: dynamicvalidate.NamespaceStringRule(func(namespace, value string) error {
        return checkReason(namespace, value)
    }),
},
```

The author implements `checkReason`, including any config access it needs.
This is an alternative implementation for the same option, not a second binding.
Do not bind the option twice. A new option requires its own binding.
Namespace message callbacks receive the annotated message field, not its parent.

#### Test the shared binding

From the Server repository root:

```console
make check-request-validation
go test -tags test_dep ./service/frontend
```

Test accepted and rejected values, namespace overrides, config changes between
calls, and missing getters. Missing implementations, duplicate bindings, and
invalid getters must fail before serving requests.

The [dynamic runner][server-dynamic] checks annotations recursively. It visits
present child messages, list elements, and message-valued map entries.
Precompilation checks their schemas even when the child is absent.
It reports paths such as `items[0].reason` and `named_items["a"].reason`.

### Publish a new rule in this order

1. Declare the unused option and `rule_spec` in API.
2. Generate and publish API-Go.
3. Update Server's API-Go dependency. Add and test one shared binding for the option.
4. Publish Server, then apply the option to fields in API.
5. Generate and publish API-Go with those field annotations.
6. Update Server's API-Go dependency and run `make check-request-validation`
   from the Server repository root.

An unused option does not need a Server implementation. An applied option on an
enrolled RPC does. API lint accepts a well-formed dynamic annotation without
checking Server support. Server CI and startup reject missing implementations
when Server adopts the annotated API-Go version. Keep shared registry startup tests.

For a shared static rule, use value-only predefined CEL. Generate API-Go and
test eager Server compilation. Reserve public extension numbers in the
Protovalidate registry before official publication.

## Checks and errors

From the API repository root, run:

```console
make validation-test
make validation-lint
buf lint
```

From the API-Go repository root, run:

```console
go test -tags protolegacy ./...
make check
```

From the Server repository root, run:

```console
make check-request-validation
go test -tags test_dep ./service/frontend
make lint-code
```

API CI rejects missing coverage on either enrolled side, no-op constraints such as `required = false`
or CEL `true`, wrong dynamic field types, unsupported list/map annotations,
and namespace rules without a singular string request namespace.
Buf's `PROTOVALIDATE` lint rule checks static rule usage. Coverage diagnostics
include the source file, line, and field or method when source information exists.

Server CI checks generated wrappers, enrollment, dynamic bindings, eager
rule compilation, and regression cases. Startup repeats discovery, binding, and rule compilation checks. A request that violates a rule returns `InvalidArgument`
with `google.rpc.BadRequest` details: field path, rule ID, and message.
Request implementation failures return `Internal`, not a client error.
Enrolled response value or runtime implementation failures only warn and record
a counter; invalid schemas and missing implementations still fail startup.

Coverage proves that each enrolled field has a decision. It cannot prove that
a chosen constraint captures all business requirements or that a delegated
validator performs its stated checks. Review exclusion reasons and test both
accepted and rejected inputs.

[api-go-rules]: https://github.com/long-nt-tran/api-go/blob/proto-annotations-gen/temporalvalidate/v1/rules.pb.go
[server-registry]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/registry.go
[server-responses]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/response.go
[server-rules]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/service/frontend/validation_rules.go
[server-rule-helpers]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/dynamicvalidate/limits.go
[server-setting]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/config.go#L231
[server-fx]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/service/frontend/fx.go#L92
[server-dynamic]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/dynamicvalidate/dynamicvalidate.go
[server-boundaries]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/services_gen.go
