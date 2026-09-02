package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func responseSchema() *descriptorpb.FileDescriptorSet {
	set := testSchema(varintOptions(testFunction, 1), methodOptions(true, ""))
	response := set.File[2].MessageType[1]
	response.Field = []*descriptorpb.FieldDescriptorProto{{
		Name: proto.String("id"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: &descriptorpb.FieldOptions{},
	}}
	response.Field[0].Options.ProtoReflect().SetUnknown(varintOptions(testFunction, 1))
	return set
}

func TestOneOptionAuditsBothMessages(t *testing.T) {
	set := responseSchema()
	if err := check(set); err != nil {
		t.Fatal(err)
	}
	for _, side := range []struct {
		index       int
		field, name string
	}{
		{0, "Request.namespace", "request"}, {1, "Response.id", "response"},
	} {
		t.Run(side.name, func(t *testing.T) {
			set := responseSchema()
			set.File[2].MessageType[side.index].Field[0].Options = nil
			if err := check(set); err == nil || !containsAll(err.Error(), side.field, side.name+")") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUnenrolledRPCDoesNotAuditEitherMessage(t *testing.T) {
	for _, options := range [][]byte{nil, methodOptions(false, "Validation remains elsewhere.")} {
		set := responseSchema()
		set.File[2].Service[0].Method[0].Options.ProtoReflect().SetUnknown(options)
		for _, message := range set.File[2].MessageType {
			message.Field[0].Options = nil
		}
		if err := check(set); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResponseFunctionsDoNotRequireNamespaceMetadata(t *testing.T) {
	set := responseSchema()
	set.File[2].MessageType[0].Field = nil
	if err := check(set); err != nil {
		t.Fatal(err)
	}
}

func TestResponseStreamingEnrollmentFails(t *testing.T) {
	set := responseSchema()
	set.File[2].Service[0].Method[0].ServerStreaming = proto.Bool(true)
	if err := check(set); err == nil || !strings.Contains(err.Error(), "only supports unary") {
		t.Fatalf("got %v", err)
	}
}
