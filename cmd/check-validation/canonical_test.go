package main

import (
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const testCanonicalMetadata = protowire.Number(108)
const testCanonicalNamespace = protowire.Number(10032)

func canonicalMetadata(names ...string) []byte {
	var data []byte
	for _, name := range names {
		data = append(data, fieldOptions(1, []byte(name))...)
	}
	return fieldOptions(testCanonicalMetadata, data)
}

func canonicalSchema(t *testing.T, rules *validate.FieldRules) *descriptorpb.FileDescriptorSet {
	t.Helper()
	set := testSchema(staticOptions(t, rules), methodOptions(true, ""))
	set.File[0].Extension = append(set.File[0].Extension, extension("canonical_rule", ".google.protobuf.FieldOptions", testCanonicalMetadata))
	declaration := set.File[0].Extension[5]
	declaration.Number = proto.Int32(int32(testCanonicalNamespace))
	declaration.Type = descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
	declaration.Options = &descriptorpb.FieldOptions{}
	declaration.Options.ProtoReflect().SetUnknown(canonicalMetadata("namespace"))
	proto.SetExtension(declaration.Options, validate.E_Predefined, &validate.PredefinedRules{Cel: []*validate.Rule{{Expression: proto.String("!rule || this.size() > 0")}}})
	return set
}

func canonicalString(enabled uint64) *validate.StringRules {
	rules := &validate.StringRules{}
	rules.ProtoReflect().SetUnknown(varintOptions(testCanonicalNamespace, enabled))
	return rules
}

func canonicalField() *validate.FieldRules {
	return &validate.FieldRules{Type: &validate.FieldRules_String_{String_: canonicalString(1)}}
}

func TestCanonicalStringRule(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		rules *validate.FieldRules
		valid bool
	}{
		{"canonical", canonicalField(), true},
		{"missing", &validate.FieldRules{}, false},
		{"duplicate inline constraint", &validate.FieldRules{Type: &validate.FieldRules_String_{String_: &validate.StringRules{MinLen: proto.Uint64(1)}}}, false},
		{"custom CEL", &validate.FieldRules{Cel: []*validate.Rule{{Expression: proto.String("this.size() > 0")}}}, false},
		{"required only", &validate.FieldRules{Required: proto.Bool(true)}, false},
		{"false option", &validate.FieldRules{Type: &validate.FieldRules_String_{String_: canonicalString(0)}}, false},
		{"non-boolean option value", &validate.FieldRules{Type: &validate.FieldRules_String_{String_: canonicalString(2)}}, false},
		{"wrong option location", &validate.FieldRules{Required: proto.Bool(true)}, false},
		{"ignore empty", &validate.FieldRules{Ignore: validate.Ignore_IGNORE_IF_ZERO_VALUE.Enum(), Type: &validate.FieldRules_String_{String_: canonicalString(1)}}, false},
		{"ignore always", &validate.FieldRules{Ignore: validate.Ignore_IGNORE_ALWAYS.Enum(), Type: &validate.FieldRules_String_{String_: canonicalString(1)}}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			set := canonicalSchema(t, scenario.rules)
			if scenario.name == "wrong option location" {
				field := set.File[2].MessageType[0].Field[0]
				field.Options.ProtoReflect().SetUnknown(append(staticOptions(t, scenario.rules), varintOptions(testCanonicalNamespace, 1)...))
			}
			err := check(set)
			if scenario.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !containsAll(err.Error(), "use canonical rule", "string.(temporalvalidate.v1.namespace) = true", "defined at temporalvalidate/v1/rules.proto") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	t.Run("additional constraints", func(t *testing.T) {
		rules := canonicalField()
		rules.GetString().MaxLen = proto.Uint64(255)
		if err := check(canonicalSchema(t, rules)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("duplicate canonical option", func(t *testing.T) {
		rules := canonicalField()
		rules.GetString().ProtoReflect().SetUnknown(append(varintOptions(testCanonicalNamespace, 1), varintOptions(testCanonicalNamespace, 1)...))
		if err := check(canonicalSchema(t, rules)); err == nil || !strings.Contains(err.Error(), "use canonical rule") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCanonicalRuleMatching(t *testing.T) {
	t.Run("alias from metadata", func(t *testing.T) {
		set := canonicalSchema(t, canonicalField())
		set.File[0].Extension[5].Options.ProtoReflect().SetUnknown(canonicalMetadata("namespace", "namespace_name"))
		field := set.File[2].MessageType[0].Field[0]
		field.Name = proto.String("namespace_name")
		if err := check(set); err != nil {
			t.Fatal(err)
		}
		field.Options = nil
		if err := check(set); err == nil || !containsAll(err.Error(), "Request.namespace_name", "use canonical rule") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("different field name", func(t *testing.T) {
		set := canonicalSchema(t, &validate.FieldRules{Required: proto.Bool(true)})
		set.File[2].MessageType[0].Field[0].Name = proto.String("label")
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("different type", func(t *testing.T) {
		set := canonicalSchema(t, &validate.FieldRules{Required: proto.Bool(true)})
		set.File[2].MessageType[0].Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unenrolled", func(t *testing.T) {
		set := canonicalSchema(t, &validate.FieldRules{})
		set.File[2].Service[0].Method[0].Options = nil
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("field coverage exclusion", func(t *testing.T) {
		set := canonicalSchema(t, &validate.FieldRules{})
		set.File[2].MessageType[0].Field[0].Options.ProtoReflect().SetUnknown(fieldOptions(testIgnoredField, []byte("Validated by the existing namespace resolver.")))
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("response only", func(t *testing.T) {
		set := canonicalSchema(t, &validate.FieldRules{})
		method := set.File[2].Service[0].Method[0]
		method.OutputType = proto.String(".temporal.api.test.v1.Request")
		method.InputType = proto.String(".temporal.api.test.v1.Other")
		set.File[2].MessageType = append(set.File[2].MessageType, &descriptorpb.DescriptorProto{Name: proto.String("Other")})
		method.Options.ProtoReflect().SetUnknown(enrollmentOptions(testResponseValidation, true, ""))
		if err := check(set); err == nil || !containsAll(err.Error(), "use canonical rule", "response)") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCanonicalCollections(t *testing.T) {
	for _, mapValue := range []bool{false, true} {
		for _, scenario := range []string{"valid", "missing", "outer ignore", "value ignore"} {
			t.Run(fmtCollection(mapValue)+"/"+scenario, func(t *testing.T) {
				item := canonicalField()
				if scenario == "missing" {
					item = &validate.FieldRules{Required: proto.Bool(true)}
				}
				if scenario == "value ignore" {
					item.Ignore = validate.Ignore_IGNORE_IF_ZERO_VALUE.Enum()
				}
				rules := &validate.FieldRules{Type: &validate.FieldRules_Repeated{Repeated: &validate.RepeatedRules{Items: item}}}
				if mapValue {
					rules.Type = &validate.FieldRules_Map{Map: &validate.MapRules{Values: item}}
				}
				if scenario == "outer ignore" {
					rules.Ignore = validate.Ignore_IGNORE_ALWAYS.Enum()
				}
				set := canonicalSchema(t, rules)
				field := set.File[2].MessageType[0].Field[0]
				field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
				if mapValue {
					field.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
					field.TypeName = proto.String(".temporal.api.test.v1.Entry")
					set.File[2].MessageType = append(set.File[2].MessageType, &descriptorpb.DescriptorProto{Name: proto.String("Entry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("key"), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}, {Name: proto.String("value"), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}}})
				}
				problems := checkCanonicalField("Request.namespace", field, buildIndex(set))
				if scenario == "valid" && len(problems) != 0 {
					t.Fatal(problems)
				}
				if scenario != "valid" && (len(problems) == 0 || !strings.Contains(problems[0], fmtCollection(mapValue)+".string")) {
					t.Fatalf("unexpected problems: %v", problems)
				}
			})
		}
	}
}

func TestCanonicalNestedField(t *testing.T) {
	const nested = protowire.Number(106)
	set := canonicalSchema(t, canonicalField())
	set.File[0].Extension = append(set.File[0].Extension, extension("validate_nested", ".google.protobuf.FieldOptions", nested))
	child := &descriptorpb.DescriptorProto{Name: proto.String("Child"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("namespace"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: &descriptorpb.FieldOptions{}}}}
	set.File[2].MessageType = append(set.File[2].MessageType, child)
	parent := &descriptorpb.FieldDescriptorProto{Name: proto.String("details"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".temporal.api.test.v1.Child"), Options: &descriptorpb.FieldOptions{}}
	parent.Options.ProtoReflect().SetUnknown(varintOptions(nested, 1))
	set.File[2].MessageType[0].Field = append(set.File[2].MessageType[0].Field, parent)
	if err := check(set); err == nil || !containsAll(err.Error(), "Child.namespace", "use canonical rule") {
		t.Fatalf("unexpected error: %v", err)
	}
	child.Field[0].Options.ProtoReflect().SetUnknown(staticOptions(t, canonicalField()))
	if err := check(set); err != nil {
		t.Fatal(err)
	}
}

func fmtCollection(mapValue bool) string {
	if mapValue {
		return "map.values"
	}
	return "repeated.items"
}

func TestCanonicalDeclarationErrors(t *testing.T) {
	for _, scenario := range []string{"empty names", "blank name", "invalid name", "duplicate alias", "duplicate metadata", "wrong wire type", "wrong extendee", "wrong rule type", "repeated rule", "missing predefined", "no-op predefined", "conflicting rule"} {
		t.Run(scenario, func(t *testing.T) {
			set := canonicalSchema(t, canonicalField())
			declaration := set.File[0].Extension[5]
			switch scenario {
			case "empty names":
				declaration.Options.ProtoReflect().SetUnknown(canonicalMetadata())
			case "blank name":
				declaration.Options.ProtoReflect().SetUnknown(canonicalMetadata(" "))
			case "invalid name":
				declaration.Options.ProtoReflect().SetUnknown(canonicalMetadata("namespace.name"))
			case "duplicate alias":
				declaration.Options.ProtoReflect().SetUnknown(canonicalMetadata("namespace", "namespace"))
			case "duplicate metadata":
				declaration.Options.ProtoReflect().SetUnknown(append(canonicalMetadata("namespace"), canonicalMetadata("namespace")...))
			case "wrong wire type":
				declaration.Options.ProtoReflect().SetUnknown(fieldOptions(testCanonicalMetadata, varintOptions(1, 1)))
			case "wrong extendee":
				declaration.Extendee = proto.String(".google.protobuf.FieldOptions")
			case "wrong rule type":
				declaration.Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
			case "repeated rule":
				declaration.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			case "missing predefined":
				proto.ClearExtension(declaration.Options, validate.E_Predefined)
			case "no-op predefined":
				proto.SetExtension(declaration.Options, validate.E_Predefined, &validate.PredefinedRules{Cel: []*validate.Rule{{Expression: proto.String("true")}}})
			case "conflicting rule":
				other := proto.Clone(declaration).(*descriptorpb.FieldDescriptorProto)
				other.Name = proto.String("other_namespace")
				other.Number = proto.Int32(int32(testCanonicalNamespace + 1))
				set.File[0].Extension = append(set.File[0].Extension, other)
			}
			if err := check(set); err == nil || !(strings.Contains(err.Error(), "canonical") || strings.Contains(err.Error(), "claimed")) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDecodedCanonicalStringExtension(t *testing.T) {
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("canonical_registered_test.proto"), Package: proto.String("canonicaltest"), Syntax: proto.String("proto2"), Dependency: []string{"buf/validate/validate.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("namespace"), Number: proto.Int32(int32(testCanonicalNamespace)), Extendee: proto.String(".buf.validate.StringRules"), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	extension := dynamicpb.NewExtensionType(file.Extensions().Get(0))
	for _, enabled := range []bool{true, false} {
		rules := &validate.FieldRules{Type: &validate.FieldRules_String_{String_: &validate.StringRules{}}}
		proto.SetExtension(rules.GetString(), extension, enabled)
		if canonicalEnabled(rules, false, false, testCanonicalNamespace) != enabled {
			t.Fatalf("registered extension enabled=%v was not respected", enabled)
		}
	}
}
