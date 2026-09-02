package main

import (
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func staticOptions(t *testing.T, rules *validate.FieldRules) []byte {
	t.Helper()
	data, err := proto.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	return fieldOptions(testFieldRules, data)
}

func TestRejectNoOpRules(t *testing.T) {
	for _, rules := range []*validate.FieldRules{
		{Required: proto.Bool(false)},
		{Ignore: validate.Ignore_IGNORE_ALWAYS.Enum()},
		{Cel: []*validate.Rule{{Id: proto.String("noop"), Message: proto.String("unused"), Expression: proto.String("true")}}},
	} {
		set := testSchema(staticOptions(t, rules), methodOptions(true, ""))
		if err := check(set); err == nil || !strings.Contains(err.Error(), "do not enforce a constraint") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestRejectRepeatedDynamicField(t *testing.T) {
	set := testSchema(varintOptions(testDynamicRule, 1), methodOptions(true, ""))
	set.File[2].MessageType[0].Field[0].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	if err := check(set); err == nil || !strings.Contains(err.Error(), "requires a singular field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNestedCoverage(t *testing.T) {
	const nested = protowire.Number(106)
	set := testSchema(staticOptions(t, &validate.FieldRules{Required: proto.Bool(true)}), methodOptions(true, ""))
	set.File[0].Extension = append(set.File[0].Extension, extension("validate_nested", ".google.protobuf.FieldOptions", nested))
	request := set.File[2].MessageType[0]
	child := &descriptorpb.DescriptorProto{Name: proto.String("Child"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("value"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}}}
	set.File[2].MessageType = append(set.File[2].MessageType, child)
	request.Field = append(request.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("child"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".temporal.api.test.v1.Child"), Options: &descriptorpb.FieldOptions{}})
	request.Field[1].Options.ProtoReflect().SetUnknown(staticOptions(t, &validate.FieldRules{Required: proto.Bool(true)}))
	if err := check(set); err == nil || !strings.Contains(err.Error(), "validate_nested") {
		t.Fatalf("missing nested coverage decision did not fail: %v", err)
	}
	request.Field[1].Options.ProtoReflect().SetUnknown(varintOptions(nested, 1))
	if err := check(set); err == nil || !strings.Contains(err.Error(), "Child.value") {
		t.Fatalf("unexpected error: %v", err)
	}
	child.Field[0].Options = &descriptorpb.FieldOptions{}
	child.Field[0].Options.ProtoReflect().SetUnknown(fieldOptions(testIgnoredField, []byte("all strings are valid")))
	if err := check(set); err != nil {
		t.Fatal(err)
	}
	child.Field = append(child.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("parent"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".temporal.api.test.v1.Request"), Options: &descriptorpb.FieldOptions{}})
	child.Field[1].Options.ProtoReflect().SetUnknown(varintOptions(nested, 1))
	if err := check(set); err != nil {
		t.Fatal(err)
	}
	request.Field[1].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	if err := check(set); err != nil {
		t.Fatal(err)
	}
}

func TestDecodedRegisteredStaticExtensionProvidesCoverage(t *testing.T) {
	set := testSchema(nil, methodOptions(true, ""))
	set.File[1].Extension[0].Number = proto.Int32(int32(validate.E_Field.TypeDescriptor().Number()))
	proto.SetExtension(set.File[2].MessageType[0].Field[0].Options, validate.E_Field, &validate.FieldRules{Required: proto.Bool(true)})
	data, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	if err := check(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestFalseBooleanConstIsAConstraint(t *testing.T) {
	set := testSchema(staticOptions(t, &validate.FieldRules{Type: &validate.FieldRules_Bool{Bool: &validate.BoolRules{Const: proto.Bool(false)}}}), methodOptions(true, ""))
	field := set.File[2].MessageType[0].Field[0]
	field.Name = proto.String("flag")
	field.Type = descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
	if err := check(set); err != nil {
		t.Fatal(err)
	}
}

func TestLocationsAndWhitespaceReasons(t *testing.T) {
	set := testSchema(fieldOptions(testIgnoredField, []byte(" \n ")), methodOptions(true, ""))
	set.File[2].SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{Path: []int32{4, 0, 2, 0}, Span: []int32{9, 0, 15}}}}
	if err := check(set); err == nil || !containsAll(err.Error(), "service.proto:10", "requires a reason") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDynamicRulesRequireEnrollment(t *testing.T) {
	set := testSchema(varintOptions(testDynamicRule, 1), nil)
	if err := check(set); err == nil || !strings.Contains(err.Error(), "not reachable from an enabled RPC") {
		t.Fatalf("unexpected error: %v", err)
	}
}
