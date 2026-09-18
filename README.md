# convertgen

Fail-closed protobuf ↔ domain Go converter generator.

## Install / run

```bash
# from a module that has protos + convert.yaml
buf build -o descriptors.binpb
go run github.com/cogitatorum/convertgen/cmd/convertgen \
  -descriptors descriptors.binpb \
  -config path/to/convert.yaml
```

`out_dir` in the YAML is resolved relative to the current working directory (usually your module root).

## Local development

Develop alongside a consumer module with a Go workspace:

```text
go.work
convertgen/
your-app/
```

## Proto options

Canonical option definitions live in `proto/convertgen/options/v1/options.proto`
(`convertgen.options.v1`: `skip`, `go_name`, `domain_type`).

Consumers should import that file (or keep a copy with the same package name and field numbers).

## Policy features

| Feature | Support |
|--------|---------|
| Enums | yes (`enums`) |
| Scalar / nested / repeated fields | yes |
| Oneofs | yes (`oneofs`) |
| `bytes` ↔ `*json.RawMessage` | yes (`convert: raw_json_ptr`) |
| `google.protobuf.Value` ↔ `*json.RawMessage` | yes (`convert: value_json_ptr`) |
| Pointer domain types | yes (`pointer: true`) |
| Getters (ToProto) | yes (`get: Status`) |
| `uuid.UUID` → string | yes (`convert: uuid_string`) |
| `error` → string | yes (`convert: error_string`) |
| ToProto-only messages | yes (`to_proto_only: true`) |
| Aggregate compose | yes (`compose`) |
| Maps / repeated enums | not yet |

Every field (and every oneof/compose entry) on a listed message must be present in policy — generation fails closed otherwise.

### Getter + convert example

```yaml
RunStep:
  domain_type: Step
  pointer: true
  to_proto_only: true
  fields:
    status: { get: Status }
    error: { get: Err, convert: error_string }
```

### Compose example

```yaml
WorkflowDefDetail:
  domain_type: WorkflowDef
  pointer: true
  compose:
    def: { message: WorkflowDef }
    steps: { get: Steps, message: StepSpec }
    edges: { get: Edges, message: Edge }
```
