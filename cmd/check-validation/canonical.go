package main

import (
	"fmt"
	"regexp"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

type canonicalRule struct {
	name     string
	number   protowire.Number
	location string
}

var protoFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func indexCanonicalRules(schema *index, declarations []dynamicExtension) {
	metadata, ok := schema.extensions[canonicalRuleName]
	if !ok {
		return
	}
	for _, declaration := range declarations {
		values, err := bytesExtension(declaration.descriptor.GetOptions(), metadata)
		if err != nil {
			schema.problems = append(schema.problems, declaration.name+": invalid canonical_rule: "+err.Error())
			continue
		}
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 {
			schema.problems = append(schema.problems, declaration.name+": canonical_rule must be set once")
			continue
		}
		if declaration.descriptor.GetExtendee() != ".buf.validate.StringRules" || declaration.descriptor.GetType() != descriptorpb.FieldDescriptorProto_TYPE_BOOL || declaration.descriptor.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			schema.problems = append(schema.problems, declaration.name+": canonical_rule requires a singular bool extension of buf.validate.StringRules")
			continue
		}
		if err := canonicalPredefinedRule(declaration.descriptor); err != nil {
			schema.problems = append(schema.problems, declaration.name+": "+err.Error())
			continue
		}
		names, err := canonicalFieldNames(values[0])
		if err != nil {
			schema.problems = append(schema.problems, declaration.name+": "+err.Error())
			continue
		}
		location := schema.locations[declaration.name]
		if location == "" {
			location = declaration.file
		}
		rule := canonicalRule{name: declaration.name, number: protowire.Number(declaration.descriptor.GetNumber()), location: location}
		for _, name := range names {
			if existing, ok := schema.canonical[name]; ok {
				schema.problems = append(schema.problems, fmt.Sprintf("%s: canonical string field %q is already claimed by %s", declaration.name, name, existing.name))
				continue
			}
			schema.canonical[name] = rule
		}
	}
}

func canonicalPredefinedRule(declaration *descriptorpb.FieldDescriptorProto) error {
	values, err := bytesExtension(declaration.GetOptions(), protowire.Number(validate.E_Predefined.TypeDescriptor().Number()))
	if err != nil || len(values) != 1 {
		return fmt.Errorf("canonical rule must define one buf.validate.predefined option")
	}
	rules := &validate.PredefinedRules{}
	if err := proto.Unmarshal(values[0], rules); err != nil {
		return fmt.Errorf("invalid canonical predefined rule: %w", err)
	}
	if !effectiveRules(rules.ProtoReflect()) {
		return fmt.Errorf("canonical predefined rule must enforce a constraint")
	}
	return nil
}

func canonicalFieldNames(data []byte) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for len(data) != 0 {
		number, kind, value, rest, err := consumeField(data)
		if err != nil {
			return nil, fmt.Errorf("invalid canonical_rule: %w", err)
		}
		data = rest
		if number != 1 || kind != protowire.BytesType {
			return nil, fmt.Errorf("canonical_rule requires string field_names")
		}
		name := string(value.([]byte))
		if !protoFieldName.MatchString(name) || seen[name] {
			return nil, fmt.Errorf("canonical_rule has an invalid or duplicate field name %q", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("canonical_rule requires at least one field name")
	}
	return names, nil
}

func checkCanonicalField(name string, field *descriptorpb.FieldDescriptorProto, schema *index) []string {
	rule, ok := schema.canonical[field.GetName()]
	if !ok {
		return nil
	}
	mapValue := canonicalMapValue(field, schema)
	if field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_STRING && !mapValue {
		return nil
	}
	values, err := bytesExtension(field.GetOptions(), schema.extensions[fieldRulesName])
	if err != nil {
		return []string{name + ": " + err.Error()}
	}
	if len(values) == 1 {
		rules := &validate.FieldRules{}
		if err := proto.Unmarshal(values[0], rules); err == nil {
			if canonicalEnabled(rules, field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED, mapValue, rule.number) {
				return nil
			}
		}
	}
	path := "string"
	if mapValue {
		path = "map.values.string"
	} else if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		path = "repeated.items.string"
	}
	return []string{fmt.Sprintf("%s: use canonical rule (buf.validate.field).%s.(%s) = true (defined at %s); additional constraints are allowed, but ignore settings must not bypass this rule", name, path, rule.name, rule.location)}
}

func canonicalMapValue(field *descriptorpb.FieldDescriptorProto, schema *index) bool {
	message := schema.messages[field.GetTypeName()]
	if field.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED || !message.GetOptions().GetMapEntry() {
		return false
	}
	for _, value := range message.GetField() {
		if value.GetName() == "value" {
			return value.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING
		}
	}
	return false
}

func canonicalEnabled(rules *validate.FieldRules, repeated, mapValue bool, number protowire.Number) bool {
	if rules.GetIgnore() != validate.Ignore_IGNORE_UNSPECIFIED {
		return false
	}
	if mapValue {
		rules = rules.GetMap().GetValues()
	} else if repeated {
		rules = rules.GetRepeated().GetItems()
	}
	if rules.GetIgnore() != validate.Ignore_IGNORE_UNSPECIFIED {
		return false
	}
	values, err := varintExtension(rules.GetString(), number)
	return err == nil && len(values) == 1 && values[0] == 1
}
