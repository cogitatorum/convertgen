package convertgen

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Config is the fail-closed convert.yaml policy.
type Config struct {
	ProtoPackage  string                `yaml:"proto_package"`
	ProtoGoImport string                `yaml:"proto_go_import"`
	OutDir        string                `yaml:"out_dir"`
	OutPackage    string                `yaml:"out_package"`
	Enums         map[string]enumPolicy `yaml:"enums"`
	Messages      map[string]msgPolicy  `yaml:"messages"`
}

type enumPolicy struct {
	DomainImport string            `yaml:"domain_import"`
	DomainType   string            `yaml:"domain_type"`
	Values       map[string]string `yaml:"values"`
}

type msgPolicy struct {
	DomainImport string                                `yaml:"domain_import"`
	DomainType   string                                `yaml:"domain_type"`
	Pointer      bool                                  `yaml:"pointer"`
	ToProtoOnly  bool                                  `yaml:"to_proto_only"`
	Plural       string                                `yaml:"plural"`
	Fields       fieldMap                              `yaml:"fields"`
	Oneofs       map[string]map[string]oneofCasePolicy `yaml:"oneofs"`
	Compose      map[string]composeFieldPolicy         `yaml:"compose"`
}

// fieldPolicy maps one proto field. Shorthand YAML string sets Field.
type fieldPolicy struct {
	Field   string `yaml:"field"`
	Get     string `yaml:"get"`
	Convert string `yaml:"convert"` // uuid_string | error_string | bytes_copy | raw_json_ptr | value_json_ptr
}

type fieldMap map[string]fieldPolicy

func (m *fieldMap) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Tag == "!!null" {
		*m = nil
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("fields must be a mapping")
	}
	out := fieldMap{}
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		var fp fieldPolicy
		switch val.Kind {
		case yaml.ScalarNode:
			fp.Field = val.Value
		default:
			if err := val.Decode(&fp); err != nil {
				return fmt.Errorf("fields.%s: %w", key, err)
			}
		}
		if fp.Field == "" && fp.Get == "" {
			return fmt.Errorf("fields.%s: field or get required", key)
		}
		out[key] = fp
	}
	*m = out
	return nil
}

type oneofCasePolicy struct {
	Field   string `yaml:"field"`
	Convert string `yaml:"convert"`
}

// composeFieldPolicy builds an aggregate ToProto from nested converters.
type composeFieldPolicy struct {
	Message string `yaml:"message"` // proto message name → MessageToProto
	Get     string `yaml:"get"`     // optional accessor; empty means pass `in` through
}

type enumModel struct {
	Name        string
	ProtoIdent  string
	DomainIdent string
	ZeroDomain  string
	Values      []enumValue
}

type enumValue struct {
	ProtoIdent  string
	DomainIdent string
}

type msgModel struct {
	Name        string
	ProtoIdent  string
	DomainIdent string // includes * if Pointer
	DomainBase  string // without pointer
	Plural      string
	Pointer     bool
	ToProtoOnly bool
	Fields      []fieldModel
	Oneofs      []oneofModel
	HasOneofs   bool
	Compose     []composeModel
	IsCompose   bool
}

type fieldModel struct {
	ProtoField  string
	DomainField string // exported field; empty if get-only
	Getter      string // accessor name without (); empty if field
	IsRepeated  bool
	Kind        fieldKind
	EnumFunc    string
	MessageFunc string
	Convert     string
}

type fieldKind string

const (
	kindScalar  fieldKind = "scalar"
	kindEnum    fieldKind = "enum"
	kindMessage fieldKind = "message"
	kindBytes   fieldKind = "bytes"
)

type oneofModel struct {
	Name   string
	Getter string
	Cases  []oneofCaseModel
}

type oneofCaseModel struct {
	CaseName     string
	WrapperType  string
	WrapperField string
	DomainField  string
	Kind         fieldKind
	MessageFunc  string
	Convert      string
}

type composeModel struct {
	ProtoField  string
	MessageFunc string
	Getter      string // empty => pass in
}

type imp struct {
	Alias string
	Path  string
}

// Options for a Generate run.
type Options struct {
	ConfigPath      string
	DescriptorsPath string
}

// Generate loads policy + descriptors and writes zz_generated.convert.go.
func Generate(opt Options) error {
	if opt.ConfigPath == "" {
		return fmt.Errorf("-config is required")
	}
	if opt.DescriptorsPath == "" {
		return fmt.Errorf("-descriptors is required (buf build -o descriptors.binpb)")
	}

	cfg, err := loadConfig(opt.ConfigPath)
	if err != nil {
		return err
	}
	files, err := loadDescriptors(opt.DescriptorsPath)
	if err != nil {
		return err
	}

	var errs []string
	enums, e := buildEnums(files, cfg)
	errs = append(errs, e...)
	msgs, e := buildMessages(files, cfg)
	errs = append(errs, e...)
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "convertgen: fail-closed errors:")
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", err)
		}
		return fmt.Errorf("%d policy error(s)", len(errs))
	}

	src, err := render(cfg, enums, msgs)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	formatted, err := formatSource(src)
	if err != nil {
		return fmt.Errorf("gofmt generated code: %w\n%s", err, src)
	}

	outDir := cfg.OutDir
	if !filepath.IsAbs(outDir) {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		outDir = filepath.Join(wd, outDir)
	}

	out := filepath.Join(outDir, "zz_generated.convert.go")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, formatted, 0o644); err != nil {
		return err
	}
	fmt.Printf("convertgen: wrote %s\n", out)
	return nil
}

func loadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.ProtoPackage == "" || cfg.ProtoGoImport == "" || cfg.OutDir == "" {
		return Config{}, fmt.Errorf("proto_package, proto_go_import, and out_dir are required")
	}
	if cfg.OutPackage == "" {
		cfg.OutPackage = "v1"
	}
	return cfg, nil
}

func loadDescriptors(path string) (*protoregistry.Files, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(raw, fds); err != nil {
		return nil, fmt.Errorf("unmarshal FileDescriptorSet: %w", err)
	}
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("protodesc.NewFiles: %w", err)
	}
	return files, nil
}
