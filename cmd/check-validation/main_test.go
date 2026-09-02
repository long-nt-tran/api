package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	testFieldRules        = protowire.Number(100)
	testIgnoredField      = protowire.Number(101)
	testRequestValidation = protowire.Number(102)
	testDynamicRule       = protowire.Number(103)
	testRuleSpec          = protowire.Number(105)
)

func TestCheck(t *testing.T) {
	t.Run("covered request", func(t *testing.T) {
		set := testSchema(fieldOptions(testFieldRules, []byte{1}), methodOptions(true, ""))
		if err := check(set, set, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("uncovered field suggests shared rule", func(t *testing.T) {
		set := testSchema(nil, methodOptions(true, ""))
		err := check(set, set, nil)
		if err == nil || !containsAll(err.Error(), "Request.namespace", "consider (buf.validate.field).string.(temporalvalidate.v1.namespace)") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("typed dynamic rule provides coverage", func(t *testing.T) {
		set := testSchema(varintOptions(testDynamicRule, 1), methodOptions(true, ""))
		if err := check(set, set, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dynamic rule rejects the wrong field type", func(t *testing.T) {
		set := testSchema(varintOptions(testDynamicRule, 1), methodOptions(true, ""))
		set.File[2].MessageType[0].Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
		err := check(set, set, nil)
		if err == nil || !containsAll(err.Error(), "dynamic_global_max_id_length requires TYPE_STRING, got TYPE_INT32") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("dynamic rule requires a Server implementation", func(t *testing.T) {
		set := testSchema(varintOptions(testDynamicRule, 1), methodOptions(true, ""))
		err := check(set, set, map[string]struct{}{})
		if err == nil || !containsAll(err.Error(), "dynamic_global_max_id_length has no registered Server implementation") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("new RPC must select validation", func(t *testing.T) {
		current := testSchema(fieldOptions(testFieldRules, []byte{1}), nil)
		baseline := testSchema(nil, nil)
		baseline.File[2].Service = nil
		err := check(current, baseline, nil)
		if err == nil || !containsAll(err.Error(), "new RPC", "enable request validation") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ignored field requires a reason", func(t *testing.T) {
		set := testSchema(fieldOptions(testIgnoredField, nil), methodOptions(true, ""))
		err := check(set, set, nil)
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

	dynamicExtension := extension("dynamic_global_max_id_length", ".google.protobuf.FieldOptions", testDynamicRule)
	dynamicExtension.Type = descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
	dynamicExtension.Options = &descriptorpb.FieldOptions{}
	dynamicExtension.Options.ProtoReflect().SetUnknown(fieldOptions(testRuleSpec, dynamicRuleSpec(1, descriptorpb.FieldDescriptorProto_TYPE_STRING)))

	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{
			Name:    proto.String("temporalvalidate/v1/rules.proto"),
			Package: proto.String("temporalvalidate.v1"),
			Extension: []*descriptorpb.FieldDescriptorProto{
				extension("field_coverage_ignored", ".google.protobuf.FieldOptions", testIgnoredField),
				extension("request_validation", ".google.protobuf.MethodOptions", testRequestValidation),
				extension("rule_spec", ".google.protobuf.FieldOptions", testRuleSpec),
				dynamicExtension,
				extension("namespace", ".buf.validate.StringRules", 104),
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
			}},
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: proto.String("TestService"),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:      proto.String("Call"),
					InputType: proto.String(".temporal.api.test.v1.Request"),
					Options:   methodOptions,
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
	var value []byte
	if enabled {
		value = protowire.AppendTag(value, 1, protowire.VarintType)
		value = protowire.AppendVarint(value, 1)
	}
	if ignored != "" {
		value = protowire.AppendTag(value, 2, protowire.BytesType)
		value = protowire.AppendString(value, ignored)
	}
	return fieldOptions(testRequestValidation, value)
}

func dynamicRuleSpec(scope uint64, fieldType descriptorpb.FieldDescriptorProto_Type) []byte {
	var value []byte
	value = protowire.AppendTag(value, 1, protowire.VarintType)
	value = protowire.AppendVarint(value, scope)
	value = protowire.AppendTag(value, 2, protowire.VarintType)
	return protowire.AppendVarint(value, uint64(fieldType))
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
