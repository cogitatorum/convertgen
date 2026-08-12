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


## Proto options

Canonical option definitions live in `options/v1/options.proto` (`skip`, `go_name`, `domain_type`).
Consumer repos should use the same package name and field numbers
