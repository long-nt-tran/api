package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	testFieldRules       = protowire.Number(100)
	testIgnoredField     = protowire.Number(101)
	testRPCValidation    = protowire.Number(102)
	testFunction         = protowire.Number(103)
	testRuleMetadata     = protowire.Number(104)
	testCanonicalIgnored = protowire.Number(105)
)

func TestCheck(t *testing.T) {
	t.Run("covered request", func(t *testing.T) {
		set := testSchema(fieldOptions(testFieldRules, []byte{0xc8, 1, 1}), methodOptions(true, ""))
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("uncovered field requires a decision", func(t *testing.T) {
		set := testSchema(nil, methodOptions(true, ""))
		err := check(set)
		if err == nil || !containsAll(err.Error(), "Request.namespace", "add a validation rule") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("symbol provides coverage from a catalog", func(t *testing.T) {
		set := testSchema(varintOptions(testFunction, 1), methodOptions(true, ""))
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("symbol rejects incompatible field types", func(t *testing.T) {
		set := testSchema(varintOptions(testFunction, 1), methodOptions(true, ""))
		field := proto.Clone(set.File[2].MessageType[0].Field[0]).(*descriptorpb.FieldDescriptorProto)
		field.Name = proto.String("count")
		field.Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
		set.File[2].MessageType[0].Field = append(set.File[2].MessageType[0].Field, field)
		if err := check(set); err == nil || !containsAll(err.Error(), "custom", "singular TYPE_STRING") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("unenrolled RPC needs no decision or field coverage", func(t *testing.T) {
		set := testSchema(nil, nil)
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("RPC exclusion is optional documentation", func(t *testing.T) {
		set := testSchema(nil, methodOptions(false, "Validation remains in the handler."))
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("RPC enrollment rejects invalid decisions", func(t *testing.T) {
		for _, options := range [][]byte{methodOptions(true, "elsewhere"), methodOptions(false, " "), methodOptions(false, ""), append(methodOptions(true, ""), methodOptions(true, "")...)} {
			set := testSchema(nil, options)
			if err := check(set); err == nil || !strings.Contains(err.Error(), "rpc_validation") {
				t.Fatalf("unexpected error: %v", err)
			}
		}
	})

	t.Run("ignored field requires a reason", func(t *testing.T) {
		set := testSchema(fieldOptions(testIgnoredField, nil), methodOptions(true, ""))
		err := check(set)
		if err == nil || !containsAll(err.Error(), "field_coverage_ignored requires a reason") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func testSchema(fieldUnknown, methodUnknown []byte) *descriptorpb.FileDescriptorSet {
	fieldOpts := &descriptorpb.FieldOptions{}
	fieldOpts.ProtoReflect().SetUnknown(fieldUnknown)
	methodOptions := &descriptorpb.MethodOptions{}
	methodOptions.ProtoReflect().SetUnknown(methodUnknown)

	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{
			Name:    proto.String("temporalvalidate/v1/annotations.proto"),
			Package: proto.String("temporalvalidate.v1"),
			Extension: []*descriptorpb.FieldDescriptorProto{
				extension("field_coverage_ignored", ".google.protobuf.FieldOptions", testIgnoredField),
				extension("rpc_validation", ".google.protobuf.MethodOptions", testRPCValidation),
				extension("rule", ".google.protobuf.FieldOptions", testRuleMetadata),
				ruleDeclaration("custom", testFunction, "ValidateCustom", descriptorpb.FieldDescriptorProto_TYPE_STRING, "", nil),
				extension("canonical_rule_ignored", ".google.protobuf.FieldOptions", testCanonicalIgnored),
			},
		},
		{
			Name:    proto.String("buf/validate/validate.proto"),
			Package: proto.String("buf.validate"),
			Extension: []*descriptorpb.FieldDescriptorProto{
				extension("field", ".google.protobuf.FieldOptions", testFieldRules),
			},
		},
		{
			Name:    proto.String("temporal/api/test/v1/service.proto"),
			Package: proto.String("temporal.api.test.v1"),
			MessageType: []*descriptorpb.DescriptorProto{{
				Name: proto.String("Request"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:    proto.String("namespace"),
					Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					Number:  proto.Int32(1),
					Options: fieldOpts,
				}},
			}, {Name: proto.String("Response")}},
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: proto.String("TestService"),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Call"),
					InputType:  proto.String(".temporal.api.test.v1.Request"),
					OutputType: proto.String(".temporal.api.test.v1.Response"),
					Options:    methodOptions,
				}},
			}},
		},
	}}
}

func extension(name, extendee string, number protowire.Number) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Extendee: proto.String(extendee),
		Number:   proto.Int32(int32(number)),
	}
}

func fieldOptions(extension protowire.Number, value []byte) []byte {
	result := protowire.AppendTag(nil, extension, protowire.BytesType)
	return protowire.AppendBytes(result, value)
}

func varintOptions(extension protowire.Number, value uint64) []byte {
	result := protowire.AppendTag(nil, extension, protowire.VarintType)
	return protowire.AppendVarint(result, value)
}

func methodOptions(enabled bool, ignored string) []byte {
	return enrollmentOptions(testRPCValidation, enabled, ignored)
}

func enrollmentOptions(extension protowire.Number, enabled bool, ignored string) []byte {
	var value []byte
	if enabled {
		value = protowire.AppendTag(value, 1, protowire.VarintType)
		value = protowire.AppendVarint(value, 1)
	}
	if ignored != "" {
		value = protowire.AppendTag(value, 2, protowire.BytesType)
		value = protowire.AppendString(value, ignored)
	}
	return fieldOptions(extension, value)
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
