# Temporal validation

This framework makes request validation part of the API schema. The first part
of this document is for proto authors. The second part is for maintainers of
the validation framework.

## Proto author guide

### 1. Enroll each new RPC

In the service proto, every new Temporal API RPC must make one choice.

Enable this framework when it owns request validation:

```protobuf
rpc StartNexusOperationExecution(StartNexusOperationExecutionRequest)
    returns (StartNexusOperationExecutionResponse) {
  option (temporalvalidate.v1.request_validation).enabled = true;
}
```

Otherwise, record where validation occurs:

```protobuf
option (temporalvalidate.v1.request_validation).ignored =
    "validated by the Nexus protocol boundary";
```

If you forget this step, `make validation-lint` and API CI fail with the RPC
name. The check compares your branch with its exact Git merge base. Existing
RPCs are unchanged until they are explicitly enrolled.

If an enabled RPC is not added to its Server handler integration, API CI can
pass, but the Server enrollment test fails.

### 2. Classify every field in an enrolled request

At the field declaration, every direct field must have one of the following:

| Field needs | Annotation to use |
| --- | --- |
| A value-only constraint | A standard Protovalidate rule |
| A value-only constraint shared by Temporal fields | A predefined rule from `temporalvalidate/v1/rules.proto` |
| A limit read from Server dynamic config | A typed `temporalvalidate.v1.dynamic_*` option |
| No validation at this boundary | `field_coverage_ignored` with a reason |

Examples:

```protobuf
message ExampleRequest {
  string namespace = 1 [
    (buf.validate.field).string.(temporalvalidate.v1.namespace) = true
  ];
  string operation_id = 2 [
    (buf.validate.field).string.(temporalvalidate.v1.operation_id) = true,
    (temporalvalidate.v1.dynamic_global_max_id_length) = true
  ];
  string endpoint = 3 [(buf.validate.field).required = true];
  bool include_input = 4 [
    (temporalvalidate.v1.field_coverage_ignored) = "all values are valid"
  ];
}
```

If you leave a field unclassified, API CI fails with the field name. It may
also suggest a shared string rule when the field name matches one. If an
exclusion is empty or is combined with a rule, API CI fails.

Coverage applies to direct request fields. A message-valued field needs a rule
or exclusion at the request boundary. Its nested fields need Protovalidate
rules when their contents require validation.

### 3. Reuse dynamic rules

Search `temporalvalidate/v1/rules.proto` before adding a dynamic rule. Reuse an
option only when its scope, field type, configuration value, and error meaning
match.

Typed options provide protobuf completion and these signals:

| Mistake | Signal |
| --- | --- |
| The option name does not exist | Protobuf compilation fails. |
| The option has the wrong field type | `make validation-lint` and API CI fail. |
| A namespace-scoped option is used on a request without a string `namespace` field | `make validation-lint` and API CI fail. |
| Temporal Server does not implement the option | API CI fails against the manifest from official Server `main`. |

If no existing option has the correct meaning, add its API and Server parts in
the order below.

### 4. Add a new dynamic rule

A dynamic rule needs a typed API option and a Server implementation. Use this
landing order:

1. In API `temporalvalidate/v1/rules.proto`, declare the boolean field option
   and its `rule_spec`. Do not apply the option yet. Invalid scope or type
   metadata fails API CI.
2. Generate and publish API-Go. This creates the Go extension symbol. Without
   it, the Server registration cannot compile.
3. In the current Server integration, add that symbol to the
   [`newDynamicValidator` map][server-rule-registry]. Implement it with a
   `GlobalStringRule`, `NamespaceStringRule`, or `NamespaceMessageRule` from
   the [generic dynamic runner][server-dynamic-runner]. A namespace rule
   receives both the request namespace and field value. Missing or mismatched
   registration fails Server precompilation.
4. Add the full option name to Server [`dynamic_rules.json`][server-manifest].
   The Server enrollment test fails if this manifest and the registration map
   differ.
5. Merge the Server change, then apply the option to a field in API. Applying
   it before Server `main` publishes it fails API CI.

Declaring an unused option does not require a Server implementation and does
not fail API CI. Applying it does. The maintainer guide below shows the Server
implementation.

### 5. Run the API checks

```console
make validation-lint
buf lint
```

To test an unmerged Server change, pass its local manifest:

```console
make validation-lint \
  SERVER_VALIDATION_RULES=/path/to/temporal/chasm/lib/nexusoperation/dynamic_rules.json
```

## Validator maintainer guide

### Repository boundary

- API defines annotations in [`rules.proto`](v1/rules.proto) and checks them in
  [`cmd/check-validation`](../cmd/check-validation).
- [API-Go generates the annotation types][api-go-annotations]. It contains no
  validation policy.
- Server contains the [generic dynamic runner][server-dynamic-runner],
  [standalone Nexus rule registry][server-rule-registry],
  [imperative validator][server-imperative-validator],
  [capability manifest][server-manifest], and
  [enrollment test][server-enrollment-test].

The current Server integration covers standalone Nexus operations. A new API
area must provide its own handler enrollment set and construction checks.

### How the Server validators are built

[`validationMessages`][server-validation-messages] derives the enrolled
request types from the `FrontendHandler` method set.
[`NewFrontendHandler`][server-handler-construction] then connects three
validation paths:

- `protoValidator` is a Protovalidate validator built with those request types
  and lazy compilation disabled. Static rules and CEL compile here.
- `dynamicValidator` is built by `newDynamicValidator(config)`. Its
  `Precompile` method verifies every dynamic annotation on those request types.
- `validator` is the existing [imperative validator][server-imperative-validator].
  It contains the hardcoded validation and normalization that remains during
  migration.

The handler's [`validateProto`][server-validate-proto] method calls the first
two. Individual handler methods then call the applicable
`validateAndNormalize*` imperative method.

### How a dynamic rule is implemented

The [reusable engine][server-dynamic-runner] supplies typed rule constructors
for global strings, namespace strings, and namespace message types. It also
scans annotations, extracts the request namespace when needed, and invokes the
registered rule.

The Nexus implementations live in the
[`newDynamicValidator` map][server-rule-registry]. Each entry connects one
generated API-Go extension symbol to one Go validation function. For example:

```go
temporalvalidatepb.E_DynamicNamespaceMaxReasonLength:
    nsLenLimit(
        config.MaxReasonLength,
        "reason exceeds the namespace's max length",
    ),
```

`nsLenLimit` builds a `NamespaceStringRule`; its closure reads the supplied
config function during request validation. Adding only the proto option does
not create this implementation. The Server manifest lists the option names in
this map; it is a checked capability list, not the implementation itself.

CEL does not read Server state. Protovalidate does not expose arbitrary host
functions, and API rules must remain portable across its runtimes. Dynamic
config therefore stays in this Go registry.

The request path is:

1. Protovalidate static rules.
2. Registered dynamic rules.
3. Existing imperative validation or normalization, when present.
4. Normal request processing.

Failures return `InvalidArgument`. Keep imperative checks during migration.
Compare accepted inputs, normalization, payload accounting, and errors before
removing an old check.

To add a shared static rule, define value-only CEL in `rules.proto`, generate
API-Go, apply it, and test eager Server construction. Reserve its public
extension number in the Protovalidate registry before publication.

### Maintenance failure signals

| Broken contract | Signal |
| --- | --- |
| API-Go was not regenerated | API-Go generation or compilation fails. |
| A dynamic option has invalid scope or type metadata | API CI fails. |
| An applied dynamic option has no published Server implementation | API CI fails. |
| A registered implementation has the wrong type, lacks namespace access, or is missing | Server precompile test and handler construction fail. |
| The Server registry and JSON manifest differ | Server [`TestValidationEnrollment`][server-enrollment-test] fails. Changing only the JSON file cannot pass. |
| API enrollment and the standalone Nexus handler method set differ | Server [`TestValidationEnrollment`][server-enrollment-test] fails. |
| Static CEL cannot compile for an enrolled request | Server validator tests and handler construction fail. |
| Imperative validation or normalization changes behavior | Existing `validator_test.go` tests fail. |
| A request violates a valid rule | Request handling returns `InvalidArgument`. |

The API downloads the manifest only after the framework exists in the merge
base. A download error fails the check.

The framework guarantees an explicit decision for every new RPC, direct field
in an enrolled request, and applied dynamic option. It cannot prove business
semantics, recurse through every nested message, or enroll existing RPCs.
Protovalidate and CEL are used without a fork.

Run the focused Server checks with:

```console
go test -tags test_dep ./common/validation/...
go test -tags test_dep ./chasm/lib/nexusoperation/...
```

[api-go-annotations]: https://github.com/long-nt-tran/api-go/tree/proto-annotations-gen/temporalvalidate/v1
[server-dynamic-runner]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/common/validation/dynamicvalidate/dynamicvalidate.go#L27-L223
[server-rule-registry]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/frontend.go#L53-L100
[server-handler-construction]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/frontend.go#L102-L135
[server-validate-proto]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/frontend.go#L422-L430
[server-imperative-validator]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/validator.go#L35-L159
[server-manifest]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/dynamic_rules.json
[server-enrollment-test]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/coverage_test.go#L18-L46
[server-validation-messages]: https://github.com/long-nt-tran/temporal/blob/proto-annotated-linter-validator/chasm/lib/nexusoperation/coverage.go#L10-L27
