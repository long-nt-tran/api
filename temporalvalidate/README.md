# RPC validation

Validation is opt-in for each unary RPC. One option enrolls both its request and
response. Requests must pass before the handler runs. Responses are checked after a successful handler call. A response failure
calls `logger.Warn(...)` and records a metric. It does not replace the result.

Use `buf.validate` for static checks. Use declared symbols from
[rules.proto](v1/rules.proto) to call Server functions, including dynamic-config
checks. API-Go generates the typed `Validator[C]` interface and dispatcher.
The catalog declares signatures and canonical field names, not function bodies.

## Complete example

Set `rpc_validation.enabled = true` once on the RPC to enroll both messages.
An RPC without the option has no checks from this mechanism.

[Example source](examples/v1/example.proto):

```protobuf
syntax = "proto3";

package temporalvalidate.examples.v1;

import "buf/validate/validate.proto";
import "google/protobuf/duration.proto";
import "temporal/api/common/v1/message.proto";
import "temporalvalidate/v1/rules.proto";
import "temporalvalidate/v1/annotations.proto";

option go_package = "go.temporal.io/api/temporalvalidate/examples/v1;examples";

// Server does not expose this example service.
service ExampleService {
  rpc StartExample(StartExampleRequest) returns (StartExampleResponse) {
    // Opt in here to enforce validation for StartExampleRequest and StartExampleResponse.
    // If the request fails validation, server will reject it and error.
    // If the response fails validation, server will warn and log but still forward it.
    option (temporalvalidate.v1.rpc_validation).enabled = true;
  }
}

message StartExampleRequest {
  // Example of a check that references multiple fields.
  // This is optional, can be done as a dynamic validator on one of the fields
  // and implement this check on the server.
  option (buf.validate.message).cel = {
    id: "example.run_selection"
    message: "run_id is required when use_latest is false"
    expression: "this.use_latest || this.run_id != ''"
  };

  // Example of a canonical validator. This symbol is declared in rules.proto and
  // calls ValidateNamespace on the server. If someone adds another string namespace
  // field, lint points them to this symbol instead of inventing another validator.
  // The plugin generates the interface; the server author implements the body.
  string namespace = 1 [(temporalvalidate.v1.namespace) = true];

  // Multiple validators can be used to validate a field.
  string operation_id = 2 [
    // Require a nonempty ID.
    (buf.validate.field).string.min_len = 1,
    // Call Server's configurable byte-length check.
    (temporalvalidate.v1.id_length) = true
  ];

  // Notice how id_length is used again here. It delegates to the same server method
  // as operation_id. Its declaration accepts strings, so the plugin will yell if
  // we apply it to another type. A typo in the symbol fails proto compilation.
  string request_id = 3 [(temporalvalidate.v1.id_length) = true];

  string run_id = 4 [
    // Check UUID format only for nonempty values. Message CEL still applies.
    (buf.validate.field).ignore = IGNORE_IF_ZERO_VALUE,
    (buf.validate.field).string.uuid = true
  ];

  // Example of ignoring validation.
  bool use_latest = 5 [
    (temporalvalidate.v1.field_coverage_ignored) = "both bool values are valid"
  ];

  // Validating nested proto. validate_nested tells the validator to defer to
  // `Details` proto validator if the field is present.
  Details details = 6 [
    // Require this message to be present.
    (buf.validate.field).required = true,
    // Audit each child field.
    (temporalvalidate.v1.validate_nested) = true
  ];

  repeated Details items = 7 [
    // Permit an empty list, with no more than 10 elements.
    (buf.validate.field).repeated.max_items = 10,
    // Audit the child fields of each element.
    (temporalvalidate.v1.validate_nested) = true
  ];

  map<string, Details> named_items = 8 [
    // Permit an empty map, with no more than 10 entries.
    (buf.validate.field).map.max_pairs = 10,
    // Require each map key to be nonempty.
    (buf.validate.field).map.keys.string.min_len = 1,
    // Audit the child fields of each value.
    (temporalvalidate.v1.validate_nested) = true
  ];

  temporal.api.common.v1.Payload input = 9 [
    // Call Server's payload validator.
    (temporalvalidate.v1.payload) = true,

    // Ignore validator for `Payload` proto.
    (temporalvalidate.v1.nested_coverage_ignored) = "ValidatePayload dynamic validator checks the field"
  ];

  // The annotation does not validate or normalize this duration.
  google.protobuf.Duration timeout = 10 [(temporalvalidate.v1.field_coverage_ignored) = "the handler validates and normalizes this timeout"];
}

message Details {
  // Require a nonempty label.
  string label = 1 [(buf.validate.field).string.min_len = 1];

  // The function can read StartExampleRequest.namespace through ctx.Request.
  // It receives the same root request in details, lists, maps, and the response.
  string reason = 2 [(temporalvalidate.v1.reason_length) = true];
}

message StartExampleResponse {
  string operation_id = 1 [
    // Require a nonempty ID.
    (buf.validate.field).string.min_len = 1,
    // Reuse the request's length validator.
    (temporalvalidate.v1.id_length) = true
  ];

  temporal.api.common.v1.Payload result = 2 [
    // The function receives the originating request, not this response, in ctx.
    (temporalvalidate.v1.payload) = true,
    (temporalvalidate.v1.nested_coverage_ignored) = "ValidatePayload dynamic validator checks the field"
  ];

  // Presence of `Details` is optional since `(buf.validate.field).required = true`
  // is not present.
  // `temporalvalidate.v1.validate_nested` audits child coverage; runtime checks run when present.
  Details details = 3 [(temporalvalidate.v1.validate_nested) = true];
}
```

The message CEL rule checks two fields together. It cannot call Server functions
or read dynamic config. A singular message field without `required = true`
can be absent. `validate_nested` audits child-field coverage; it does not require
the parent to be present. Coverage annotations do not disable runtime checks.

## Server functions

Declare a symbol once in [rules.proto](v1/rules.proto). For example:

```protobuf
extend google.protobuf.FieldOptions {
  optional bool namespace = 901001 [(rule) = {
    function: "ValidateNamespace"
    field_type: TYPE_STRING
    canonical_field_names: "namespace"
  }];
}
```

The RPC author uses `(temporalvalidate.v1.namespace) = true`, not a function-name
string. Proto compilation catches unknown symbols. The declaration determines
the server method and its parameter type. For message or enum rules, also set
`type_name` to the exact fully-qualified protobuf name, without a leading dot.

The implementation still decides checks, config keys, empty-value behavior, and
namespace handling. Metadata does not execute a check or a CEL expression.

### Canonical reuse

`canonical_field_names` steers matching fields in enrolled requests and responses
to this symbol. A string `namespace` cannot replace it with inline `min_len`,
custom CEL, or an alternative symbol. Additional constraints alongside it are
fine. Each function has one owning symbol; new declarations cannot duplicate
that function or claim an existing canonical name.

If the field genuinely has different semantics, add
`(temporalvalidate.v1.canonical_rule_ignored) = "specific reason"` and supply
its own validation rule. This skips canonical lint only, not coverage or runtime
checks. `field_coverage_ignored` delegates the whole field, including canonical
coverage. Unenrolled RPCs do not need canonical annotations.

The [API-Go plugin](https://github.com/long-nt-tran/api-go/tree/proto-annotations-gen/cmd/protoc-gen-temporalvalidate)
generates this contract from declared rules selected by enrolled RPCs:

```go
type Validator[C any] interface {
    ValidateIDLength(C, string) error
    ValidateNamespace(C, string) error
    ValidatePayload(C, *commonpb.Payload) error
    ValidateReasonLength(C, string) error
    // Other enrolled fields add methods.
}
```

Method names must start with `Validate` and an uppercase letter. Rule symbols
must be optional bool FieldOptions extensions and must be set to `true` when
selected. API lint and the plugin reject invalid declarations, wrong field types,
duplicate function ownership, and conflicting canonical names. Select symbols
on singular fields. For list elements or map values, annotate their child fields.

If a method already exists on Server, a new use requires no Server change.
For a new rule, declare its symbol and signature in API, regenerate API-Go,
then add its method to the existing
[Server validator](https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/service/frontend/validation_rules.go).
For these example fields, the relevant methods can look like this:

```go
// The framework supplies the original RPC request, also for response checks.
type ValidationContext struct {
    Request proto.Message
}

func (v *Validator) ValidateNamespace(_ validation.ValidationContext, value string) error {
    if value == "" {
        return errors.New("namespace is required")
    }
    return nil
}

func (v *Validator) ValidateIDLength(_ validation.ValidationContext, value string) error {
    if len(value) > v.maxIDLength() {
        return errors.New("ID exceeds the configured byte limit")
    }
    return nil // This implementation permits an empty value.
}

func (v *Validator) ValidateReasonLength(ctx validation.ValidationContext, value string) error {
    request, ok := ctx.Request.(interface{ GetNamespace() string })
    if !ok || request.GetNamespace() == "" {
        return errors.New("request namespace is required")
    }
    if len(value) > v.maxReasonLength(request.GetNamespace()) {
        return errors.New("reason exceeds the configured byte limit")
    }
    return nil
}

func (v *Validator) ValidatePayload(ctx validation.ValidationContext, value *commonpb.Payload) error {
    if value == nil {
        return nil // This implementation permits an absent payload.
    }
    request, ok := ctx.Request.(interface{ GetNamespace() string })
    if !ok || request.GetNamespace() == "" {
        return errors.New("request namespace is required")
    }
    if value.Size() > v.maxPayloadSize(request.GetNamespace()) {
        return errors.New("payload exceeds the configured size limit")
    }
    return nil
}
```

The getter fields are dynamic-config getters initialized by the existing Server
provider. See that provider for config keys and types. It returns the generated
`Validator[ValidationContext]` interface. Missing methods and wrong signatures
do not compile. No method bodies are generated. The compiler cannot prove a
body performs a useful check; test the implementation.

The context contains only `Request`. The framework does not extract a namespace.
The author reads it from the request when needed. Nested fields, list elements,
map values, and response fields receive the root request. A request without
a namespace cannot support these example namespace-based checks.

Functions receive zero scalar values and absent message values (`nil`).
Their implementations decide whether these values are valid. Static ignore
options do not skip function calls.

## Commands

Run each block from the named repository root.

API:

```sh
buf lint
make validation-test validation-lint
```

API-Go, after updating the `proto/api` submodule to the API commit:

```sh
make proto
make generatorcheck
make check test
```

The plugin supports the protoc plugin protocol. Normal generation runs the same
generator against the API descriptor set.

Server, after updating `go.mod` to the generated API-Go commit:

```sh
go generate ./common/validation
make check-request-validation
```

No owner-specific coverage file, manifest, callback registration, or wrapper is
needed. Generated service boundaries discover enrollment from API descriptors.
Static-only validation requires no Server implementation.

## Coverage and failures

Each field reachable from an enrolled RPC's request or response needs a static
rule, a function, or
`field_coverage_ignored` with a reason. Message-valued fields also need
`validate_nested` or `nested_coverage_ignored`. Exclusions record decisions;
they do not implement checks.

Unenrolled RPCs, new or existing, need no exclusion annotation. To document an
excluded RPC, use `rpc_validation.ignored` with a reason. This excludes both
messages. Do not combine `enabled` and `ignored`. There is no request-only or
response-only enrollment option.

API lint checks coverage and function names/types. API-Go generation checks
inferred signatures. Server compilation checks method completeness. Startup
compiles static rules and checks function descriptors. Regenerate and test all
three repositories after changing annotations.

Response checks run only after successful unary calls. They use a response copy
and an original-request snapshot. Warnings contain bounded field paths and rule
IDs, not raw values. Response validation does not replace authorization,
redaction, or checks required before sending data.
