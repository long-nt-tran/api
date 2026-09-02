// check-validation checks RPC validation coverage in a protobuf descriptor set.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	fieldRulesName       = "buf.validate.field"
	ignoredFieldName     = "temporalvalidate.v1.field_coverage_ignored"
	nestedFieldName      = "temporalvalidate.v1.validate_nested"
	ignoredNestedName    = "temporalvalidate.v1.nested_coverage_ignored"
	rpcValidationName    = "temporalvalidate.v1.rpc_validation"
	ruleMetadataName     = "temporalvalidate.v1.rule"
	canonicalIgnoredName = "temporalvalidate.v1.canonical_rule_ignored"
)

type index struct {
	extensions map[string]protowire.Number
	messages   map[string]*descriptorpb.DescriptorProto
	methods    map[string]*descriptorpb.MethodDescriptorProto
	problems   []string
	locations  map[string]string
	rules      map[string]validatorRule
	canonical  map[string]string
}

func main() {
	descriptorSet := flag.String("descriptor-set", "", "current FileDescriptorSet")
	flag.Parse()
	if *descriptorSet == "" {
		fmt.Fprintln(os.Stderr, "--descriptor-set is required")
		os.Exit(2)
	}

	current, err := readDescriptorSet(*descriptorSet)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := check(current); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readDescriptorSet(path string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return set, nil
}

func check(currentSet *descriptorpb.FileDescriptorSet) error {
	current := buildIndex(currentSet)
	current.problems = append(current.problems, collectRules(currentSet, current)...)

	required := []string{fieldRulesName, ignoredFieldName, rpcValidationName, ruleMetadataName}
	for _, name := range required {
		if _, ok := current.extensions[name]; !ok {
			return fmt.Errorf("validation schema does not define %s", name)
		}
	}

	problems := append([]string(nil), current.problems...)
	methodNames := sortedKeys(current.methods)
	for _, methodName := range methodNames {
		method := current.methods[methodName]
		mode, err := validationMode(method.GetOptions(), current.extensions[rpcValidationName], "rpc_validation")
		if err != nil {
			problems = append(problems, methodName+": "+err.Error())
			continue
		}
		if !mode.enabled {
			continue
		}
		if method.GetClientStreaming() || method.GetServerStreaming() {
			problems = append(problems, methodName+": RPC validation only supports unary RPCs")
			continue
		}
		for _, side := range []struct{ name, messageType string }{
			{"request", method.GetInputType()}, {"response", method.GetOutputType()},
		} {
			message := current.messages[side.messageType]
			if message == nil {
				problems = append(problems, methodName+": "+side.name+" message "+side.messageType+" was not found")
				continue
			}
			fieldProblems := checkNestedFields(side.messageType, message, current, map[string]bool{})
			for _, problem := range fieldProblems {
				problems = append(problems, problem+" ("+methodName+" "+side.name+")")
			}
		}
	}
	problems = append(problems, checkFunctions(current)...)

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	unique := problems[:0]
	for _, problem := range problems {
		if len(unique) == 0 || unique[len(unique)-1] != problem {
			unique = append(unique, problem)
		}
	}
	problems = unique
	for i, problem := range problems {
		name, _, _ := strings.Cut(problem, ":")
		if location := current.locations[name]; location != "" {
			problems[i] = location + ": " + problem
		}
	}
	return errors.New("RPC validation check failed:\n\t" + strings.Join(problems, "\n\t"))
}

func indexLocations(schema *index, file *descriptorpb.FileDescriptorProto) {
	locations := map[string]string{}
	for _, location := range file.GetSourceCodeInfo().GetLocation() {
		if len(location.GetSpan()) > 0 {
			locations[fmt.Sprint(location.GetPath())] = fmt.Sprintf("%s:%d", file.GetName(), location.GetSpan()[0]+1)
		}
	}
	var walk func(string, *descriptorpb.DescriptorProto, []int32)
	walk = func(name string, message *descriptorpb.DescriptorProto, path []int32) {
		for i, field := range message.GetField() {
			fieldPath := append(append([]int32(nil), path...), 2, int32(i))
			schema.locations[name+"."+field.GetName()] = locations[fmt.Sprint(fieldPath)]
		}
		for i, nested := range message.GetNestedType() {
			nestedPath := append(append([]int32(nil), path...), 3, int32(i))
			walk(name+"."+nested.GetName(), nested, nestedPath)
		}
	}
	for i, message := range file.GetMessageType() {
		walk(joinName(file.GetPackage(), message.GetName()), message, []int32{4, int32(i)})
	}
	for i, service := range file.GetService() {
		for j, method := range service.GetMethod() {
			name := joinName(joinName(file.GetPackage(), service.GetName()), method.GetName())
			schema.locations[name] = locations[fmt.Sprint([]int32{6, int32(i), 2, int32(j)})]
		}
	}
	for i, extension := range file.GetExtension() {
		name := joinName(file.GetPackage(), extension.GetName())
		schema.locations[name] = locations[fmt.Sprint([]int32{7, int32(i)})]
	}
}

type methodMode struct {
	enabled bool
}

func validationMode(options *descriptorpb.MethodOptions, extension protowire.Number, name string) (methodMode, error) {
	values, err := bytesExtension(options, extension)
	if err != nil || len(values) == 0 {
		return methodMode{}, err
	}
	if len(values) != 1 {
		return methodMode{}, fmt.Errorf("%s must be set once", name)
	}

	var enabled bool
	var ignored string
	data := values[0]
	for len(data) != 0 {
		number, wireType, value, rest, err := consumeField(data)
		if err != nil {
			return methodMode{}, fmt.Errorf("invalid %s: %w", name, err)
		}
		data = rest
		switch number {
		case 1:
			if wireType != protowire.VarintType {
				return methodMode{}, fmt.Errorf("%s.enabled has the wrong wire type", name)
			}
			enabled = value.(uint64) != 0
		case 2:
			if wireType != protowire.BytesType {
				return methodMode{}, fmt.Errorf("%s.ignored has the wrong wire type", name)
			}
			ignored = strings.TrimSpace(string(value.([]byte)))
		}
	}

	if enabled && ignored != "" {
		return methodMode{}, fmt.Errorf("%s cannot be enabled and ignored", name)
	}
	if !enabled && ignored == "" {
		return methodMode{}, fmt.Errorf("%s must be enabled or include an exclusion reason", name)
	}
	return methodMode{enabled: enabled}, nil
}

func checkNestedFields(messageName string, message *descriptorpb.DescriptorProto, schema *index, visited map[string]bool) []string {
	if visited[messageName] {
		return nil
	}
	visited[messageName] = true
	var problems []string
	fieldRules := schema.extensions[fieldRulesName]
	ignoredField := schema.extensions[ignoredFieldName]

	for _, field := range message.GetField() {
		name := strings.TrimPrefix(messageName, ".") + "." + field.GetName()
		staticValues, staticErr := bytesExtension(field.GetOptions(), fieldRules)
		ignoredValues, ignoredErr := bytesExtension(field.GetOptions(), ignoredField)
		functions, functionErr := fieldFunctions(field, schema)
		nestedValues, nestedErr := varintExtension(field.GetOptions(), schema.extensions[nestedFieldName])
		ignoredNested, ignoredNestedErr := bytesExtension(field.GetOptions(), schema.extensions[ignoredNestedName])
		for _, err := range []error{nestedErr, ignoredNestedErr} {
			if err != nil {
				problems = append(problems, name+": "+err.Error())
			}
		}
		nested := len(nestedValues) > 0 && nestedValues[0] == 1
		if len(nestedValues) > 0 && !nested {
			problems = append(problems, name+": validate_nested must be true when set")
		}
		nestedReason := ""
		if len(ignoredNested) > 0 {
			nestedReason = strings.TrimSpace(string(ignoredNested[0]))
			if nestedReason == "" {
				problems = append(problems, name+": nested_coverage_ignored requires a reason")
			}
		}
		if nested && nestedReason != "" {
			problems = append(problems, name+": cannot combine validate_nested with nested_coverage_ignored")
		}
		if staticErr != nil {
			problems = append(problems, name+": "+staticErr.Error())
		}
		if ignoredErr != nil {
			problems = append(problems, name+": "+ignoredErr.Error())
		}
		if functionErr != nil {
			problems = append(problems, name+": "+functionErr.Error())
		}

		hasStatic := false
		for _, value := range staticValues {
			rules := &validate.FieldRules{}
			if err := proto.Unmarshal(value, rules); err != nil {
				problems = append(problems, name+": invalid static rules: "+err.Error())
				continue
			}
			hasStatic = hasStatic || rules.GetIgnore() != validate.Ignore_IGNORE_ALWAYS && effectiveRules(rules.ProtoReflect())
		}
		if len(staticValues) > 0 && !hasStatic {
			problems = append(problems, name+": static rules do not enforce a constraint; use field_coverage_ignored with a reason")
		}
		hasRule := hasStatic || len(functions) != 0 || nested
		ignored := ""
		for _, value := range ignoredValues {
			ignored = strings.TrimSpace(string(value))
		}
		if len(ignoredValues) != 0 && ignored == "" {
			problems = append(problems, name+": field_coverage_ignored requires a reason")
		}
		if ignored != "" && hasRule {
			problems = append(problems, name+": cannot combine validation rules with field_coverage_ignored")
		}
		if err := checkCanonical(field, schema, functions, ignored != ""); err != nil {
			problems = append(problems, name+": "+err.Error())
		}
		if field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
			if len(nestedValues) > 0 || len(ignoredNested) > 0 {
				problems = append(problems, name+": nested coverage options require a message field")
			}
		} else if ignored == "" {
			if !nested && nestedReason == "" {
				problems = append(problems, name+": enable validate_nested or add a nested_coverage_ignored reason")
			}
			if nested {
				childName := field.GetTypeName()
				child := schema.messages[childName]
				if child != nil && child.GetOptions().GetMapEntry() {
					for _, entryField := range child.GetField() {
						if entryField.GetName() == "value" {
							childName = entryField.GetTypeName()
							child = schema.messages[childName]
							break
						}
					}
				}
				if child == nil {
					problems = append(problems, name+": validate_nested requires message values")
				} else {
					problems = append(problems, checkNestedFields(childName, child, schema, visited)...)
				}
			}
		}
		if ignored == "" && !hasRule {
			problem := name + ": add a validation rule or field_coverage_ignored reason"

			problems = append(problems, problem)
		}
	}
	return problems
}

func effectiveRules(message protoreflect.Message) bool {
	effective := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		name := string(field.Name())
		if name == "ignore" || name == "example" {
			return true
		}
		if field.IsList() {
			for i := 0; i < value.List().Len(); i++ {
				item := value.List().Get(i)
				if field.Kind() == protoreflect.MessageKind {
					effective = effective || effectiveRules(item.Message())
				} else if field.Kind() == protoreflect.StringKind {
					expression := strings.TrimSpace(item.String())
					effective = effective || expression != "" && expression != "true"
				} else {
					effective = true
				}
			}
		} else if field.Kind() == protoreflect.MessageKind {
			effective = effective || effectiveRules(value.Message())
		} else if field.Kind() == protoreflect.BoolKind {
			effective = effective || value.Bool() || name == "const"
		} else if name == "id" || name == "message" {
			return true
		} else if name == "expression" {
			expression := strings.TrimSpace(value.String())
			effective = effective || expression != "" && expression != "true"
		} else if name == "min_len" || name == "min_bytes" || name == "min_items" || name == "min_pairs" {
			effective = effective || value.Uint() > 0
		} else {
			effective = true
		}
		return true
	})
	data := message.GetUnknown()
	for len(data) > 0 {
		_, kind, value, rest, err := consumeField(data)
		if err != nil {
			return false
		}
		data = rest
		effective = effective || kind == protowire.VarintType && value.(uint64) != 0
	}
	return effective
}

func buildIndex(set *descriptorpb.FileDescriptorSet) *index {
	result := &index{
		extensions: make(map[string]protowire.Number),
		messages:   make(map[string]*descriptorpb.DescriptorProto),
		methods:    make(map[string]*descriptorpb.MethodDescriptorProto),
		locations:  make(map[string]string),
	}
	if set == nil {
		return result
	}
	for _, file := range set.GetFile() {
		indexLocations(result, file)
		prefix := file.GetPackage()
		for _, extension := range file.GetExtension() {
			name := joinName(prefix, extension.GetName())
			number := protowire.Number(extension.GetNumber())
			result.extensions[name] = number

		}
		for _, message := range file.GetMessageType() {
			indexMessage(result.messages, "."+joinName(prefix, message.GetName()), message)
		}
		for _, service := range file.GetService() {
			serviceName := joinName(prefix, service.GetName())
			for _, method := range service.GetMethod() {
				name := joinName(serviceName, method.GetName())
				result.methods[name] = method
			}
		}
	}
	return result
}

func indexMessage(messages map[string]*descriptorpb.DescriptorProto, name string, message *descriptorpb.DescriptorProto) {
	messages[name] = message
	for _, nested := range message.GetNestedType() {
		indexMessage(messages, name+"."+nested.GetName(), nested)
	}
}

func joinName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func bytesExtension(message proto.Message, extension protowire.Number) ([][]byte, error) {
	if message == nil {
		return nil, nil
	}
	var values [][]byte
	var registeredErr error
	proto.RangeExtensions(message, func(kind protoreflect.ExtensionType, value any) bool {
		if protowire.Number(kind.TypeDescriptor().Number()) != extension {
			return true
		}
		switch value := value.(type) {
		case proto.Message:
			data, err := proto.Marshal(value)
			registeredErr = err
			values = append(values, data)
		case string:
			values = append(values, []byte(value))
		case []byte:
			values = append(values, value)
		default:
			registeredErr = fmt.Errorf("extension %d has the wrong type", extension)
		}
		return false
	})
	if registeredErr != nil {
		return nil, registeredErr
	}
	data := message.ProtoReflect().GetUnknown()
	for len(data) != 0 {
		number, wireType, value, rest, err := consumeField(data)
		if err != nil {
			return nil, err
		}
		data = rest
		if number != extension {
			continue
		}
		if wireType != protowire.BytesType {
			return nil, fmt.Errorf("extension %d has the wrong wire type", extension)
		}
		values = append(values, value.([]byte))
	}
	return values, nil
}

func varintExtension(message proto.Message, extension protowire.Number) ([]uint64, error) {
	if message == nil {
		return nil, nil
	}
	var values []uint64
	var registeredErr error
	proto.RangeExtensions(message, func(kind protoreflect.ExtensionType, value any) bool {
		if protowire.Number(kind.TypeDescriptor().Number()) != extension {
			return true
		}
		if enabled, ok := value.(bool); ok {
			if enabled {
				values = append(values, 1)
			} else {
				values = append(values, 0)
			}
		} else {
			registeredErr = fmt.Errorf("extension %d has the wrong type", extension)
		}
		return false
	})
	if registeredErr != nil {
		return nil, registeredErr
	}
	data := message.ProtoReflect().GetUnknown()
	for len(data) != 0 {
		number, wireType, value, rest, err := consumeField(data)
		if err != nil {
			return nil, err
		}
		data = rest
		if number != extension {
			continue
		}
		if wireType != protowire.VarintType {
			return nil, fmt.Errorf("extension %d has the wrong wire type", extension)
		}
		values = append(values, value.(uint64))
	}
	return values, nil
}

func consumeField(data []byte) (protowire.Number, protowire.Type, any, []byte, error) {
	number, wireType, tagLength := protowire.ConsumeTag(data)
	if tagLength < 0 {
		return 0, 0, nil, nil, protowire.ParseError(tagLength)
	}
	data = data[tagLength:]
	switch wireType {
	case protowire.VarintType:
		value, length := protowire.ConsumeVarint(data)
		if length < 0 {
			return 0, 0, nil, nil, protowire.ParseError(length)
		}
		return number, wireType, value, data[length:], nil
	case protowire.BytesType:
		value, length := protowire.ConsumeBytes(data)
		if length < 0 {
			return 0, 0, nil, nil, protowire.ParseError(length)
		}
		return number, wireType, value, data[length:], nil
	default:
		length := protowire.ConsumeFieldValue(number, wireType, data)
		if length < 0 {
			return 0, 0, nil, nil, protowire.ParseError(length)
		}
		return number, wireType, nil, data[length:], nil
	}
}
