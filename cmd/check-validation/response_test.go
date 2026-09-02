package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func responseSchema() *descriptorpb.FileDescriptorSet {
	set := testSchema(nil, enrollmentOptions(testResponseValidation, true, ""))
	response := &descriptorpb.DescriptorProto{Name: proto.String("Response"), Field: []*descriptorpb.FieldDescriptorProto{{
		Name: proto.String("id"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: &descriptorpb.FieldOptions{},
	}}}
	response.Field[0].Options.ProtoReflect().SetUnknown(varintOptions(testDynamicRule, 1))
	set.File[2].MessageType = append(set.File[2].MessageType, response)
	set.File[2].Service[0].Method[0].OutputType = proto.String(".temporal.api.test.v1.Response")
	return set
}

func TestResponseEnrollment(t *testing.T) {
	t.Run("response only does not audit request", func(t *testing.T) {
		set := responseSchema()
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("request and response are independent", func(t *testing.T) {
		set := responseSchema()
		set.File[2].Service[0].Method[0].Options.ProtoReflect().SetUnknown(append(methodOptions(true, ""), enrollmentOptions(testResponseValidation, true, "")...))
		if err := check(set); err == nil || !strings.Contains(err.Error(), "Request.namespace") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("response fields need coverage", func(t *testing.T) {
		set := responseSchema()
		set.File[2].MessageType[1].Field[0].Options = nil
		if err := check(set); err == nil || !containsAll(err.Error(), "Response.id", "response)") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("unenrolled response is not audited", func(t *testing.T) {
		set := responseSchema()
		set.File[2].Service[0].Method[0].Options = nil
		set.File[2].MessageType[1].Field[0].Options = nil
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("response exclusion is optional documentation", func(t *testing.T) {
		set := responseSchema()
		set.File[2].Service[0].Method[0].Options.ProtoReflect().SetUnknown(enrollmentOptions(testResponseValidation, false, "Response contains opaque worker data."))
		set.File[2].MessageType[1].Field[0].Options = nil
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("response enrollment rejects invalid decisions", func(t *testing.T) {
		for _, options := range [][]byte{enrollmentOptions(testResponseValidation, true, "elsewhere"), enrollmentOptions(testResponseValidation, false, " "), append(enrollmentOptions(testResponseValidation, true, ""), enrollmentOptions(testResponseValidation, true, "")...)} {
			set := responseSchema()
			set.File[2].Service[0].Method[0].Options.ProtoReflect().SetUnknown(options)
			if err := check(set); err == nil || !strings.Contains(err.Error(), "response_validation") {
				t.Fatalf("unexpected error: %v", err)
			}
		}
	})
}

func TestResponseNamespaceComesOnlyFromRequest(t *testing.T) {
	for _, scenario := range []string{"valid", "absent", "repeated", "wrong type"} {
		t.Run(scenario, func(t *testing.T) {
			set := responseSchema()
			rule := set.File[0].Extension[4]
			rule.Name = proto.String("dynamic_namespace_max_reason_length")
			rule.Options.ProtoReflect().SetUnknown(fieldOptions(testRuleSpec, dynamicRuleSpec(2, descriptorpb.FieldDescriptorProto_TYPE_STRING)))
			request := set.File[2].MessageType[0]
			switch scenario {
			case "absent":
				request.Field = nil
			case "repeated":
				request.Field[0].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			case "wrong type":
				request.Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
			}
			err := check(set)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "request message to have a string namespace") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
