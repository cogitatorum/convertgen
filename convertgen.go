package convertgen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	optionsv1 "github.com/cogitatorum/convertgen/options/v1"
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
	DomainImport string            `yaml:"domain_import"`
	DomainType   string            `yaml:"domain_type"`
	Fields       map[string]string `yaml:"fields"`
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
	DomainIdent string
	Fields      []fieldModel
}

type fieldModel struct {
	ProtoField  string
	DomainField string
	IsEnum      bool
	EnumFunc    string
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
	formatted, err := format.Source(src)
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

func buildEnums(files *protoregistry.Files, cfg Config) ([]enumModel, []string) {
	names := sortedKeys(cfg.Enums)
	var out []enumModel
	var errs []string
	protoAlias := aliasFor(cfg.ProtoGoImport)

	for _, name := range names {
		pol := cfg.Enums[name]
		ed, err := findEnum(files, cfg.ProtoPackage+"."+name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("enum %s: %v", name, err))
			continue
		}
		if pol.DomainImport == "" || pol.DomainType == "" {
			errs = append(errs, fmt.Sprintf("enum %s: domain_import and domain_type required", name))
			continue
		}
		domAlias := aliasFor(pol.DomainImport)
		m := enumModel{
			Name:        name,
			ProtoIdent:  protoAlias + "." + name,
			DomainIdent: domAlias + "." + pol.DomainType,
			ZeroDomain:  zeroValue(pol.DomainImport, pol.DomainType),
		}

		policyHit := map[string]bool{}
		for k := range pol.Values {
			policyHit[k] = false
		}

		vals := ed.Values()
		for i := 0; i < vals.Len(); i++ {
			v := vals.Get(i)
			vname := string(v.Name())
			if strings.Contains(vname, "UNSPECIFIED") {
				continue
			}
			domVal, ok := pol.Values[vname]
			if !ok {
				errs = append(errs, fmt.Sprintf("enum %s: proto value %s not in policy", name, vname))
				continue
			}
			policyHit[vname] = true
			m.Values = append(m.Values, enumValue{
				ProtoIdent:  protoAlias + "." + name + "_" + vname,
				DomainIdent: domAlias + "." + domVal,
			})
		}
		for k, hit := range policyHit {
			if !hit {
				errs = append(errs, fmt.Sprintf("enum %s: policy value %s not on proto enum", name, k))
			}
		}
		out = append(out, m)
	}
	return out, errs
}

func buildMessages(files *protoregistry.Files, cfg Config) ([]msgModel, []string) {
	names := sortedKeys(cfg.Messages)
	var out []msgModel
	var errs []string
	protoAlias := aliasFor(cfg.ProtoGoImport)

	for _, name := range names {
		pol := cfg.Messages[name]
		md, err := findMessage(files, cfg.ProtoPackage+"."+name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("message %s: %v", name, err))
			continue
		}
		if md.Oneofs().Len() > 0 {
			errs = append(errs, fmt.Sprintf("message %s: oneof not supported yet (omit from policy)", name))
			continue
		}
		if pol.DomainImport == "" || pol.DomainType == "" {
			errs = append(errs, fmt.Sprintf("message %s: domain_import and domain_type required", name))
			continue
		}

		domAlias := aliasFor(pol.DomainImport)
		m := msgModel{
			Name:        name,
			ProtoIdent:  protoAlias + "." + name,
			DomainIdent: domAlias + "." + pol.DomainType,
		}

		policyHit := map[string]bool{}
		for k := range pol.Fields {
			policyHit[k] = false
		}

		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			fname := string(fd.Name())

			if fieldSkip(fd) {
				continue
			}

			domField, ok := pol.Fields[fname]
			if !ok {
				errs = append(errs, fmt.Sprintf("message %s.%s: missing from policy", name, fname))
				continue
			}
			policyHit[fname] = true

			if goName := fieldGoName(fd); goName != "" && goName != domField {
				errs = append(errs, fmt.Sprintf("message %s.%s: policy %q != option go_name %q", name, fname, domField, goName))
				continue
			}

			if fd.IsList() || fd.IsMap() {
				errs = append(errs, fmt.Sprintf("message %s.%s: repeated/map not supported yet", name, fname))
				continue
			}

			fm := fieldModel{
				ProtoField:  snakeToCamel(fname),
				DomainField: domField,
			}
			switch fd.Kind() {
			case protoreflect.EnumKind:
				enumName := string(fd.Enum().Name())
				if _, ok := cfg.Enums[enumName]; !ok {
					errs = append(errs, fmt.Sprintf("message %s.%s: enum %s not in policy.enums", name, fname, enumName))
					continue
				}
				fm.IsEnum = true
				fm.EnumFunc = enumName
			case protoreflect.MessageKind:
				errs = append(errs, fmt.Sprintf("message %s.%s: nested message not supported yet", name, fname))
				continue
			}
			m.Fields = append(m.Fields, fm)
		}

		for k, hit := range policyHit {
			if !hit {
				errs = append(errs, fmt.Sprintf("message %s: policy field %s not on proto message", name, k))
			}
		}
		out = append(out, m)
	}
	return out, errs
}

func fieldSkip(fd protoreflect.FieldDescriptor) bool {
	fo := resolveFieldOptions(fd)
	if fo == nil {
		return false
	}
	skip, _ := proto.GetExtension(fo, optionsv1.E_Skip).(bool)
	return skip
}

func fieldGoName(fd protoreflect.FieldDescriptor) string {
	fo := resolveFieldOptions(fd)
	if fo == nil {
		return ""
	}
	goName, _ := proto.GetExtension(fo, optionsv1.E_GoName).(string)
	return goName
}

// resolveFieldOptions re-interprets custom options that often sit in unknown
// fields when descriptors come from a FileDescriptorSet.
func resolveFieldOptions(fd protoreflect.FieldDescriptor) *descriptorpb.FieldOptions {
	optsMsg := fd.Options()
	if optsMsg == nil {
		return nil
	}
	fo, ok := optsMsg.(*descriptorpb.FieldOptions)
	if !ok || fo == nil {
		return nil
	}
	raw, err := proto.Marshal(fo)
	if err != nil {
		return fo
	}
	out := &descriptorpb.FieldOptions{}
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, out); err != nil {
		return fo
	}
	return out
}

func findEnum(files *protoregistry.Files, full string) (protoreflect.EnumDescriptor, error) {
	d, err := files.FindDescriptorByName(protoreflect.FullName(full))
	if err != nil {
		return nil, err
	}
	ed, ok := d.(protoreflect.EnumDescriptor)
	if !ok {
		return nil, fmt.Errorf("not an enum")
	}
	return ed, nil
}

func findMessage(files *protoregistry.Files, full string) (protoreflect.MessageDescriptor, error) {
	d, err := files.FindDescriptorByName(protoreflect.FullName(full))
	if err != nil {
		return nil, err
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("not a message")
	}
	return md, nil
}

func render(cfg Config, enums []enumModel, msgs []msgModel) ([]byte, error) {
	impMap := map[string]string{
		cfg.ProtoGoImport: aliasFor(cfg.ProtoGoImport),
	}
	for _, pol := range cfg.Enums {
		impMap[pol.DomainImport] = aliasFor(pol.DomainImport)
	}
	for _, pol := range cfg.Messages {
		impMap[pol.DomainImport] = aliasFor(pol.DomainImport)
	}
	needFmt := len(enums) > 0 || len(msgs) > 0

	var imps []imp
	for path, alias := range impMap {
		imps = append(imps, imp{Alias: alias, Path: path})
	}
	sort.Slice(imps, func(i, j int) bool { return imps[i].Path < imps[j].Path })

	t := template.Must(template.New("gen").Parse(genTmpl))
	var buf bytes.Buffer
	err := t.Execute(&buf, map[string]any{
		"Package":  cfg.OutPackage,
		"Imports":  imps,
		"NeedFmt":  needFmt,
		"Enums":    enums,
		"Messages": msgs,
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const genTmpl = `// Code generated by convertgen. DO NOT EDIT.

package {{.Package}}

import (
{{- if .NeedFmt}}
	"fmt"
{{end}}
{{- range .Imports}}
	{{.Alias}} {{printf "%q" .Path}}
{{- end}}
)
{{range .Enums}}
func {{.Name}}ToProto(v {{.DomainIdent}}) {{.ProtoIdent}} {
	switch v {
{{- range .Values}}
	case {{.DomainIdent}}:
		return {{.ProtoIdent}}
{{- end}}
	default:
		return 0
	}
}

func {{.Name}}FromProto(v {{.ProtoIdent}}) ({{.DomainIdent}}, error) {
	switch v {
{{- range .Values}}
	case {{.ProtoIdent}}:
		return {{.DomainIdent}}, nil
{{- end}}
	default:
		return {{.ZeroDomain}}, fmt.Errorf("{{.Name}}: unsupported proto value %v", v)
	}
}
{{end}}
{{range .Messages}}
func {{.Name}}ToProto(in {{.DomainIdent}}) *{{.ProtoIdent}} {
	out := &{{.ProtoIdent}}{
{{- range .Fields}}
{{- if .IsEnum}}
		{{.ProtoField}}: {{.EnumFunc}}ToProto(in.{{.DomainField}}),
{{- else}}
		{{.ProtoField}}: in.{{.DomainField}},
{{- end}}
{{- end}}
	}
	return out
}

func {{.Name}}FromProto(in *{{.ProtoIdent}}) ({{.DomainIdent}}, error) {
	var out {{.DomainIdent}}
	if in == nil {
		return out, fmt.Errorf("{{.Name}}: nil")
	}
{{- range .Fields}}
{{- if .IsEnum}}
	{
		v, err := {{.EnumFunc}}FromProto(in.{{.ProtoField}})
		if err != nil {
			return out, err
		}
		out.{{.DomainField}} = v
	}
{{- else}}
	out.{{.DomainField}} = in.{{.ProtoField}}
{{- end}}
{{- end}}
	return out, nil
}
{{end}}
`

func aliasFor(path string) string {
	switch {
	case strings.HasSuffix(path, "/engine"):
		return "engine"
	case strings.HasSuffix(path, "/args"):
		return "args"
	case strings.HasSuffix(path, "/db"):
		return "db"
	case strings.HasSuffix(path, "/workflows/v1"):
		return "workflowv1"
	default:
		base := filepath.Base(path)
		if base == "v1" || base == "v2" {
			// github.com/.../workflows/v1 → workflowsv1-ish; use parent+version
			parent := filepath.Base(filepath.Dir(path))
			return parent + base
		}
		return base
	}
}

func zeroValue(domainImport, domainType string) string {
	if strings.HasSuffix(domainImport, "/engine") || strings.HasSuffix(domainImport, "/db") {
		return `""`
	}
	return fmt.Sprintf("%s.%s{}", aliasFor(domainImport), domainType)
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
