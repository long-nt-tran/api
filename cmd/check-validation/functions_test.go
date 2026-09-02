package main

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"strings"
	"testing"
)

func ruleDeclaration(name string, number protowire.Number, function string, typ descriptorpb.FieldDescriptorProto_Type, typeName string, canonical []string) *descriptorpb.FieldDescriptorProto {
	d := extension(name, ".google.protobuf.FieldOptions", number)
	d.Type = descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
	d.Label = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	metadata := fieldOptions(1, []byte(function))
	metadata = append(metadata, varintOptions(2, uint64(typ))...)
	if typeName != "" {
		metadata = append(metadata, fieldOptions(3, []byte(typeName))...)
	}
	for _, name := range canonical {
		metadata = append(metadata, fieldOptions(4, []byte(name))...)
	}
	d.Options = &descriptorpb.FieldOptions{}
	d.Options.ProtoReflect().SetUnknown(fieldOptions(testRuleMetadata, metadata))
	return d
}

func TestRuleDeclarations(t *testing.T) {
	for _, scenario := range []string{"valid", "bad function", "missing type", "duplicate function", "duplicate canonical", "duplicate alias", "invalid alias", "wrong extension", "false selection", "repeated selection", "wrong scalar", "wrong message", "metadata on field"} {
		t.Run(scenario, func(t *testing.T) {
			set := testSchema(varintOptions(testFunction, 1), methodOptions(true, ""))
			declarations := &set.File[0].Extension
			d := (*declarations)[3]
			field := set.File[2].MessageType[0].Field[0]
			switch scenario {
			case "bad function":
				*declarations = append((*declarations)[:3], ruleDeclaration("custom", testFunction, "validateCustom", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", nil), (*declarations)[4])
			case "missing type":
				*declarations = append((*declarations)[:3], ruleDeclaration("custom", testFunction, "ValidateCustom", 0, "", nil), (*declarations)[4])
			case "duplicate function":
				*declarations = append(*declarations, ruleDeclaration("other", 110, "ValidateCustom", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", nil))
			case "duplicate canonical":
				*declarations = append(*declarations, ruleDeclaration("first", 110, "ValidateFirst", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", []string{"namespace"}), ruleDeclaration("second", 111, "ValidateSecond", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", []string{"namespace"}))
			case "duplicate alias":
				*declarations = append(*declarations, ruleDeclaration("other", 110, "ValidateOther", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", []string{"a", "a"}))
			case "invalid alias":
				*declarations = append(*declarations, ruleDeclaration("other", 110, "ValidateOther", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", []string{"bad-name"}))
			case "wrong extension":
				d.Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
			case "false selection":
				field.Options.ProtoReflect().SetUnknown(varintOptions(testFunction, 0))
			case "repeated selection":
				field.Options.ProtoReflect().SetUnknown(append(varintOptions(testFunction, 1), varintOptions(testFunction, 1)...))
			case "wrong scalar":
				field.Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
			case "wrong message":
				*declarations = append((*declarations)[:3], ruleDeclaration("custom", testFunction, "ValidateCustom", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, "test.First", nil), (*declarations)[4])
				field.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
				field.TypeName = proto.String(".test.Second")
			case "metadata on field":
				field.Options = proto.Clone(d.Options).(*descriptorpb.FieldOptions)
			}
			err := check(set)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}

func TestCanonicalSymbols(t *testing.T) {
	for _, scenario := range []string{"selected", "inline replacement", "alternate symbol", "exemption", "empty exemption", "full exclusion", "unenrolled", "response"} {
		t.Run(scenario, func(t *testing.T) {
			set := testSchema(varintOptions(testFunction, 1), methodOptions(true, ""))
			set.File[0].Extension[3] = ruleDeclaration("custom", testFunction, "ValidateCustom", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", []string{"namespace"})
			field := set.File[2].MessageType[0].Field[0]
			switch scenario {
			case "inline replacement":
				field.Options.ProtoReflect().SetUnknown(fieldOptions(testFieldRules, []byte{0xc8, 1, 1}))
			case "alternate symbol":
				set.File[0].Extension = append(set.File[0].Extension, ruleDeclaration("alternate", 110, "ValidateAlternate", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", nil))
				field.Options.ProtoReflect().SetUnknown(varintOptions(110, 1))
			case "exemption":
				field.Options.ProtoReflect().SetUnknown(append(fieldOptions(testFieldRules, []byte{0xc8, 1, 1}), fieldOptions(testCanonicalIgnored, []byte("different concept"))...))
			case "empty exemption":
				field.Options.ProtoReflect().SetUnknown(fieldOptions(testCanonicalIgnored, []byte(" ")))
			case "full exclusion":
				field.Options.ProtoReflect().SetUnknown(fieldOptions(testIgnoredField, []byte("handled elsewhere")))
			case "unenrolled":
				set.File[2].Service[0].Method[0].Options = nil
				field.Options = nil
			case "response":
				set.File[2].MessageType[1].Field = []*descriptorpb.FieldDescriptorProto{proto.Clone(field).(*descriptorpb.FieldDescriptorProto)}
				set.File[2].MessageType[1].Field[0].Options = nil
			}
			err := check(set)
			wantError := scenario == "inline replacement" || scenario == "alternate symbol" || scenario == "empty exemption" || scenario == "response"
			if (err != nil) != wantError {
				t.Fatalf("%s: %v", scenario, err)
			}
			if scenario == "inline replacement" && !strings.Contains(err.Error(), "use canonical rule") {
				t.Fatal(err)
			}
		})
	}
}
