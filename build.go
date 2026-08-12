package convertgen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	optionsv1 "github.com/cogitatorum/convertgen/options/v1"
)

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
		if pol.DomainImport == "" || pol.DomainType == "" {
			errs = append(errs, fmt.Sprintf("message %s: domain_import and domain_type required", name))
			continue
		}

		domAlias := aliasFor(pol.DomainImport)
		domainBase := domAlias + "." + pol.DomainType
		domainIdent := domainBase
		if pol.Pointer {
			domainIdent = "*" + domainBase
		}
		plural := pol.Plural
		if plural == "" {
			plural = name + "s"
		}
		m := msgModel{
			Name:        name,
			ProtoIdent:  protoAlias + "." + name,
			DomainIdent: domainIdent,
			DomainBase:  domainBase,
			Plural:      plural,
			Pointer:     pol.Pointer,
			ToProtoOnly: pol.ToProtoOnly,
		}

		if len(pol.Compose) > 0 {
			if len(pol.Fields) > 0 || len(pol.Oneofs) > 0 {
				errs = append(errs, fmt.Sprintf("message %s: compose cannot mix with fields/oneofs", name))
				continue
			}
			cm, e := buildCompose(cfg, name, md, pol)
			errs = append(errs, e...)
			m.Compose = cm
			m.IsCompose = true
			m.ToProtoOnly = true
			out = append(out, m)
			continue
		}

		policyFieldHit := map[string]bool{}
		for k := range pol.Fields {
			policyFieldHit[k] = false
		}

		oneofByName := map[string]*oneofModel{}
		policyOneofHit := map[string]map[string]bool{}
		for onoName, cases := range pol.Oneofs {
			policyOneofHit[onoName] = map[string]bool{}
			for caseName := range cases {
				policyOneofHit[onoName][caseName] = false
			}
			oneofByName[onoName] = &oneofModel{
				Name:   onoName,
				Getter: "Get" + snakeToCamel(onoName),
			}
		}

		hasGetterOnly := false
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			fname := string(fd.Name())

			if fieldSkip(fd) {
				continue
			}
			if fd.IsMap() {
				errs = append(errs, fmt.Sprintf("message %s.%s: map not supported yet", name, fname))
				continue
			}

			if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
				onoName := string(od.Name())
				casePol, ok := pol.Oneofs[onoName][fname]
				if !ok {
					errs = append(errs, fmt.Sprintf("message %s oneof %s.%s: missing from policy.oneofs", name, onoName, fname))
					continue
				}
				if casePol.Field == "" {
					errs = append(errs, fmt.Sprintf("message %s oneof %s.%s: field required", name, onoName, fname))
					continue
				}
				policyOneofHit[onoName][fname] = true
				om := oneofByName[onoName]
				cm, e := buildOneofCase(cfg, protoAlias, name, fname, fd, casePol)
				if e != "" {
					errs = append(errs, e)
					continue
				}
				om.Cases = append(om.Cases, cm)
				continue
			}

			fpol, ok := pol.Fields[fname]
			if !ok {
				errs = append(errs, fmt.Sprintf("message %s.%s: missing from policy.fields", name, fname))
				continue
			}
			policyFieldHit[fname] = true

			domainName := fpol.Field
			if domainName == "" {
				domainName = fpol.Get
			}
			if goName := fieldGoName(fd); goName != "" && goName != domainName {
				errs = append(errs, fmt.Sprintf("message %s.%s: policy %q != option go_name %q", name, fname, domainName, goName))
				continue
			}

			fm, e := buildField(cfg, name, fname, fpol, fd)
			if e != "" {
				errs = append(errs, e)
				continue
			}
			if fm.Getter != "" && fm.DomainField == "" {
				hasGetterOnly = true
			}
			m.Fields = append(m.Fields, fm)
		}

		if hasGetterOnly && !pol.ToProtoOnly {
			errs = append(errs, fmt.Sprintf("message %s: getter-only fields require to_proto_only: true", name))
		}

		for k, hit := range policyFieldHit {
			if !hit {
				errs = append(errs, fmt.Sprintf("message %s: policy field %s not on proto message (or is a oneof case)", name, k))
			}
		}
		for onoName, cases := range policyOneofHit {
			for caseName, hit := range cases {
				if !hit {
					errs = append(errs, fmt.Sprintf("message %s: policy oneof %s.%s not on proto message", name, onoName, caseName))
				}
			}
		}
		oneofs := md.Oneofs()
		for i := 0; i < oneofs.Len(); i++ {
			od := oneofs.Get(i)
			if od.IsSynthetic() {
				continue
			}
			onoName := string(od.Name())
			if _, ok := pol.Oneofs[onoName]; !ok {
				errs = append(errs, fmt.Sprintf("message %s: proto oneof %s missing from policy.oneofs", name, onoName))
			}
		}
		for _, onoName := range sortedKeys(oneofByName) {
			m.Oneofs = append(m.Oneofs, *oneofByName[onoName])
		}
		m.HasOneofs = len(m.Oneofs) > 0
		out = append(out, m)
	}
	return out, errs
}

func buildCompose(cfg Config, msgName string, md protoreflect.MessageDescriptor, pol msgPolicy) ([]composeModel, []string) {
	var out []composeModel
	var errs []string
	policyHit := map[string]bool{}
	for k := range pol.Compose {
		policyHit[k] = false
	}

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		fname := string(fd.Name())
		if fieldSkip(fd) {
			continue
		}
		cpol, ok := pol.Compose[fname]
		if !ok {
			errs = append(errs, fmt.Sprintf("message %s.%s: missing from policy.compose", msgName, fname))
			continue
		}
		policyHit[fname] = true
		if cpol.Message == "" {
			errs = append(errs, fmt.Sprintf("message %s.%s: compose.message required", msgName, fname))
			continue
		}
		if _, ok := cfg.Messages[cpol.Message]; !ok {
			errs = append(errs, fmt.Sprintf("message %s.%s: compose message %s not in policy.messages", msgName, fname, cpol.Message))
			continue
		}
		if fd.IsList() {
			// repeated → FoosToProto(in.Get())
			if cpol.Get == "" {
				errs = append(errs, fmt.Sprintf("message %s.%s: repeated compose field requires get", msgName, fname))
				continue
			}
		}
		out = append(out, composeModel{
			ProtoField:  snakeToCamel(fname),
			MessageFunc: cpol.Message,
			Getter:      cpol.Get,
		})
	}
	for k, hit := range policyHit {
		if !hit {
			errs = append(errs, fmt.Sprintf("message %s: policy compose field %s not on proto message", msgName, k))
		}
	}
	return out, errs
}

func buildField(cfg Config, msgName, fname string, pol fieldPolicy, fd protoreflect.FieldDescriptor) (fieldModel, string) {
	fm := fieldModel{
		ProtoField:  snakeToCamel(fname),
		DomainField: pol.Field,
		Getter:      pol.Get,
		IsRepeated:  fd.IsList(),
		Kind:        kindScalar,
		Convert:     pol.Convert,
	}
	switch pol.Convert {
	case "", "uuid_string", "error_string", "bytes_copy":
	default:
		return fm, fmt.Sprintf("message %s.%s: unknown convert %q", msgName, fname, pol.Convert)
	}
	if pol.Convert == "error_string" && pol.Get == "" {
		return fm, fmt.Sprintf("message %s.%s: error_string requires get", msgName, fname)
	}
	if pol.Convert == "uuid_string" && pol.Get == "" && pol.Field == "" {
		return fm, fmt.Sprintf("message %s.%s: uuid_string requires field or get", msgName, fname)
	}

	switch fd.Kind() {
	case protoreflect.BytesKind:
		fm.Kind = kindBytes
		if pol.Convert == "" {
			fm.Convert = "bytes_copy"
		}
	case protoreflect.EnumKind:
		if fd.IsList() {
			return fm, fmt.Sprintf("message %s.%s: repeated enum not supported yet", msgName, fname)
		}
		enumName := string(fd.Enum().Name())
		if _, ok := cfg.Enums[enumName]; !ok {
			return fm, fmt.Sprintf("message %s.%s: enum %s not in policy.enums", msgName, fname, enumName)
		}
		fm.Kind = kindEnum
		fm.EnumFunc = enumName
	case protoreflect.MessageKind:
		msgType := string(fd.Message().Name())
		if _, ok := cfg.Messages[msgType]; !ok {
			return fm, fmt.Sprintf("message %s.%s: nested message %s not in policy.messages", msgName, fname, msgType)
		}
		fm.Kind = kindMessage
		fm.MessageFunc = msgType
	default:
		if fd.IsList() && fd.Kind() != protoreflect.StringKind {
			return fm, fmt.Sprintf("message %s.%s: repeated %s not supported yet (only string)", msgName, fname, fd.Kind())
		}
	}
	return fm, ""
}

func buildOneofCase(cfg Config, protoAlias, msgName, fname string, fd protoreflect.FieldDescriptor, pol oneofCasePolicy) (oneofCaseModel, string) {
	cm := oneofCaseModel{
		CaseName:     fname,
		WrapperType:  protoAlias + "." + msgName + "_" + snakeToCamel(fname),
		WrapperField: snakeToCamel(fname),
		DomainField:  pol.Field,
		Kind:         kindScalar,
		Convert:      pol.Convert,
	}
	if fd.IsList() || fd.IsMap() {
		return cm, fmt.Sprintf("message %s oneof case %s: repeated/map not supported in oneof", msgName, fname)
	}
	switch fd.Kind() {
	case protoreflect.BytesKind:
		cm.Kind = kindBytes
		if pol.Convert != "" && pol.Convert != "raw_json_ptr" {
			return cm, fmt.Sprintf("message %s oneof case %s: unknown convert %q", msgName, fname, pol.Convert)
		}
	case protoreflect.EnumKind:
		return cm, fmt.Sprintf("message %s oneof case %s: enum in oneof not supported yet", msgName, fname)
	case protoreflect.MessageKind:
		msgType := string(fd.Message().Name())
		if _, ok := cfg.Messages[msgType]; !ok {
			return cm, fmt.Sprintf("message %s oneof case %s: nested message %s not in policy.messages", msgName, fname, msgType)
		}
		cm.Kind = kindMessage
		cm.MessageFunc = msgType
		if pol.Convert != "" {
			return cm, fmt.Sprintf("message %s oneof case %s: convert not valid for message", msgName, fname)
		}
	default:
		if pol.Convert != "" {
			return cm, fmt.Sprintf("message %s oneof case %s: convert not valid for scalar", msgName, fname)
		}
	}
	return cm, ""
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
