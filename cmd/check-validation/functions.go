package main

import (
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

var validatorName = regexp.MustCompile(`^Validate[A-Z][A-Za-z0-9_]*$`)
var canonicalFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type validatorRule struct {
	symbol    string
	function  string
	fieldType descriptorpb.FieldDescriptorProto_Type
	typeName  string
	canonical []string
	number    protowire.Number
}

func collectRules(set *descriptorpb.FileDescriptorSet, schema *index) []string {
	schema.rules = map[string]validatorRule{}
	schema.canonical = map[string]string{}
	var problems []string
	functions := map[string]string{}
	inspect := func(prefix string, declaration *descriptorpb.FieldDescriptorProto) {
		symbol := joinName(prefix, declaration.GetName())
		values, err := bytesExtension(declaration.GetOptions(), schema.extensions[ruleMetadataName])
		if err != nil {
			problems = append(problems, symbol+": "+err.Error())
			return
		}
		if len(values) == 0 {
			if strings.HasPrefix(symbol, "temporalvalidate.v1.") && declaration.GetExtendee() == ".google.protobuf.FieldOptions" && declaration.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BOOL && declaration.GetName() != "validate_nested" {
				problems = append(problems, symbol+": validation symbol has no rule metadata")
			}
			return
		}
		if len(values) != 1 || declaration.GetExtendee() != ".google.protobuf.FieldOptions" || declaration.GetType() != descriptorpb.FieldDescriptorProto_TYPE_BOOL || declaration.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL {
			problems = append(problems, symbol+": rule must describe a singular bool FieldOptions extension")
			return
		}
		rule, err := parseRule(values[0])
		if err != nil {
			problems = append(problems, symbol+": "+err.Error())
			return
		}
		rule.symbol, rule.number = symbol, protowire.Number(declaration.GetNumber())
		if previous, ok := functions[rule.function]; ok {
			problems = append(problems, fmt.Sprintf("%s: server function %s is already owned by %s; reuse that symbol", symbol, rule.function, previous))
		} else {
			functions[rule.function] = symbol
		}
		schema.rules[symbol] = rule
		for _, name := range rule.canonical {
			if previous, ok := schema.canonical[name]; ok {
				problems = append(problems, fmt.Sprintf("%s: canonical field %q is already claimed by %s", symbol, name, previous))
			} else {
				schema.canonical[name] = symbol
			}
		}
	}
	var walk func(string, *descriptorpb.DescriptorProto)
	walk = func(prefix string, message *descriptorpb.DescriptorProto) {
		prefix = joinName(prefix, message.GetName())
		for _, field := range message.GetField() {
			values, _ := bytesExtension(field.GetOptions(), schema.extensions[ruleMetadataName])
			if len(values) != 0 {
				problems = append(problems, prefix+"."+field.GetName()+": rule metadata belongs on a bool FieldOptions extension")
			}
		}
		for _, extension := range message.GetExtension() {
			inspect(prefix, extension)
		}
		for _, child := range message.GetNestedType() {
			walk(prefix, child)
		}
	}
	for _, file := range set.GetFile() {
		for _, extension := range file.GetExtension() {
			symbol := joinName(file.GetPackage(), extension.GetName())
			if schema.locations[symbol] == "" {
				schema.locations[symbol] = file.GetName()
			}
			inspect(file.GetPackage(), extension)
		}
		for _, message := range file.GetMessageType() {
			walk(file.GetPackage(), message)
		}
	}
	return problems
}

func parseRule(data []byte) (validatorRule, error) {
	var rule validatorRule
	seen := map[protowire.Number]bool{}
	for len(data) != 0 {
		number, kind, value, rest, err := consumeField(data)
		if err != nil {
			return rule, err
		}
		data = rest
		if number != 4 && seen[number] {
			return rule, fmt.Errorf("rule metadata field %d is repeated", number)
		}
		seen[number] = true
		switch number {
		case 1, 3, 4:
			if kind != protowire.BytesType {
				return rule, fmt.Errorf("rule metadata field %d must be a string", number)
			}
			text := string(value.([]byte))
			switch number {
			case 1:
				rule.function = text
			case 3:
				rule.typeName = text
			case 4:
				rule.canonical = append(rule.canonical, text)
			}
		case 2:
			if kind != protowire.VarintType {
				return rule, fmt.Errorf("rule field_type must be an enum")
			}
			raw := value.(uint64)
			if raw < 1 || raw > 18 || raw == 10 {
				return rule, fmt.Errorf("rule requires a scalar or message field_type")
			}
			rule.fieldType = descriptorpb.FieldDescriptorProto_Type(raw)
		default:
			return rule, fmt.Errorf("unknown rule metadata field %d", number)
		}
	}
	if !validatorName.MatchString(rule.function) {
		return rule, fmt.Errorf("rule function must name one exported Validate* method")
	}
	if rule.fieldType < descriptorpb.FieldDescriptorProto_TYPE_DOUBLE || rule.fieldType > descriptorpb.FieldDescriptorProto_TYPE_SINT64 || rule.fieldType == descriptorpb.FieldDescriptorProto_TYPE_GROUP {
		return rule, fmt.Errorf("rule requires a scalar or message field_type")
	}
	named := rule.fieldType == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE || rule.fieldType == descriptorpb.FieldDescriptorProto_TYPE_ENUM
	if named != (rule.typeName != "") || rule.typeName != "" && !protoreflect.FullName(rule.typeName).IsValid() {
		return rule, fmt.Errorf("message/enum rules require an exact type_name without a leading dot; scalar rules must omit it")
	}
	names := map[string]bool{}
	for _, name := range rule.canonical {
		if !canonicalFieldName.MatchString(name) || names[name] {
			return rule, fmt.Errorf("invalid or duplicate canonical field name %q", name)
		}
		names[name] = true
	}
	return rule, nil
}

func fieldFunctions(field *descriptorpb.FieldDescriptorProto, schema *index) ([]string, error) {
	var symbols []string
	for _, symbol := range sortedKeys(schema.rules) {
		rule := schema.rules[symbol]
		values, err := varintExtension(field.GetOptions(), rule.number)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", symbol, err)
		}
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] != 1 {
			return nil, fmt.Errorf("%s must be true when set", symbol)
		}
		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED || field.GetType() != rule.fieldType || strings.TrimPrefix(field.GetTypeName(), ".") != rule.typeName {
			return nil, fmt.Errorf("%s requires a singular %s %s field", symbol, rule.fieldType, rule.typeName)
		}
		symbols = append(symbols, symbol)
	}
	return symbols, nil
}

func checkCanonical(field *descriptorpb.FieldDescriptorProto, schema *index, symbols []string, ignored bool) error {
	exemptions, err := bytesExtension(field.GetOptions(), schema.extensions[canonicalIgnoredName])
	if err != nil {
		return err
	}
	if len(exemptions) != 0 {
		if len(exemptions) != 1 || strings.TrimSpace(string(exemptions[0])) == "" {
			return fmt.Errorf("canonical_rule_ignored requires a reason")
		}
		return nil
	}
	if ignored {
		return nil
	}
	symbol := schema.canonical[field.GetName()]
	if symbol == "" {
		return nil
	}
	rule := schema.rules[symbol]
	if field.GetType() != rule.fieldType || strings.TrimPrefix(field.GetTypeName(), ".") != rule.typeName {
		return nil
	}
	for _, selected := range symbols {
		if selected == symbol {
			return nil
		}
	}
	return fmt.Errorf("use canonical rule (%s) = true (defined at %s); add canonical_rule_ignored with a reason for different semantics", symbol, schema.locations[symbol])
}

func checkFunctions(schema *index) []string {
	var problems []string
	for _, name := range sortedKeys(schema.messages) {
		for _, field := range schema.messages[name].GetField() {
			if _, err := fieldFunctions(field, schema); err != nil {
				problems = append(problems, name[1:]+"."+field.GetName()+": "+err.Error())
			}
		}
	}
	return problems
}
