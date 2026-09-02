// check-validation checks request validation coverage in a protobuf descriptor set.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	fieldRulesName        = "buf.validate.field"
	ignoredFieldName      = "temporalvalidate.v1.field_coverage_ignored"
	requestValidationName = "temporalvalidate.v1.request_validation"
	ruleSpecName          = "temporalvalidate.v1.rule_spec"
	dynamicRulePrefix     = "temporalvalidate.v1.dynamic_"
)

type dynamicRule struct {
	name        string
	scope       uint64
	fieldType   descriptorpb.FieldDescriptorProto_Type
	messageType string
}

type dynamicExtension struct {
	name       string
	descriptor *descriptorpb.FieldDescriptorProto
}

type index struct {
	extensions  map[string]protowire.Number
	messages    map[string]*descriptorpb.DescriptorProto
	methods     map[string]*descriptorpb.MethodDescriptorProto
	methodFile  map[string]string
	suggestions map[string]string
	dynamic     map[protowire.Number]dynamicRule
	problems    []string
}

func main() {
	descriptorSet := flag.String("descriptor-set", "", "current FileDescriptorSet")
	against := flag.String("against", "", "baseline FileDescriptorSet")
	serverRules := flag.String("server-rules", "", "JSON list of dynamic rules implemented by Temporal Server")
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
	var baseline *descriptorpb.FileDescriptorSet
	if *against != "" {
		baseline, err = readDescriptorSet(*against)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var supportedDynamicRules map[string]struct{}
	if *serverRules != "" {
		supportedDynamicRules, err = readServerRules(*serverRules)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := check(current, baseline, supportedDynamicRules); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readServerRules(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Server rules %s: %w", path, err)
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("decode Server rules %s: %w", path, err)
	}
	rules := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return nil, errors.New("Server rules contain an empty name")
		}
		if _, duplicate := rules[name]; duplicate {
			return nil, fmt.Errorf("Server rules contain duplicate %s", name)
		}
		rules[name] = struct{}{}
	}
	return rules, nil
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

func check(currentSet, baselineSet *descriptorpb.FileDescriptorSet, supportedDynamicRules map[string]struct{}) error {
	current := buildIndex(currentSet)
	baseline := buildIndex(baselineSet)

	required := []string{fieldRulesName, ignoredFieldName, requestValidationName}
	for _, name := range required {
		if _, ok := current.extensions[name]; !ok {
			return fmt.Errorf("validation schema does not define %s", name)
		}
	}

	problems := append([]string(nil), current.problems...)
	methodNames := sortedKeys(current.methods)
	for _, methodName := range methodNames {
		method := current.methods[methodName]
		mode, err := requestMode(method.GetOptions(), current.extensions[requestValidationName])
		if err != nil {
			problems = append(problems, methodName+": "+err.Error())
			continue
		}
		_, existed := baseline.methods[methodName]
		if !existed && isTemporalAPIFile(current.methodFile[methodName]) && !mode.present {
			problems = append(problems, methodName+": new RPC must enable request validation or record an exclusion reason")
			continue
		}
		if !mode.enabled {
			continue
		}
		message := current.messages[method.GetInputType()]
		if message == nil {
			problems = append(problems, methodName+": request message "+method.GetInputType()+" was not found")
			continue
		}
		problems = append(problems, checkFields(method.GetInputType(), message, current, supportedDynamicRules)...)
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("request validation check failed:\n\t" + strings.Join(problems, "\n\t"))
}

type methodMode struct {
	present bool
	enabled bool
}

func requestMode(options *descriptorpb.MethodOptions, extension protowire.Number) (methodMode, error) {
	values, err := bytesExtension(options, extension)
	if err != nil || len(values) == 0 {
		return methodMode{}, err
	}
	if len(values) != 1 {
		return methodMode{}, errors.New("request_validation must be set once")
	}

	var enabled bool
	var ignored string
	data := values[0]
	for len(data) != 0 {
		number, wireType, value, rest, err := consumeField(data)
		if err != nil {
			return methodMode{}, fmt.Errorf("invalid request_validation: %w", err)
		}
		data = rest
		switch number {
		case 1:
			if wireType != protowire.VarintType {
				return methodMode{}, errors.New("request_validation.enabled has the wrong wire type")
			}
			enabled = value.(uint64) != 0
		case 2:
			if wireType != protowire.BytesType {
				return methodMode{}, errors.New("request_validation.ignored has the wrong wire type")
			}
			ignored = string(value.([]byte))
		}
	}

	if enabled && ignored != "" {
		return methodMode{}, errors.New("request_validation cannot be enabled and ignored")
	}
	if !enabled && ignored == "" {
		return methodMode{}, errors.New("request_validation must be enabled or include an exclusion reason")
	}
	return methodMode{present: true, enabled: enabled}, nil
}

func checkFields(messageName string, message *descriptorpb.DescriptorProto, schema *index, supportedDynamicRules map[string]struct{}) []string {
	var problems []string
	fieldRules := schema.extensions[fieldRulesName]
	ignoredField := schema.extensions[ignoredFieldName]

	for _, field := range message.GetField() {
		name := strings.TrimPrefix(messageName, ".") + "." + field.GetName()
		staticValues, staticErr := bytesExtension(field.GetOptions(), fieldRules)
		ignoredValues, ignoredErr := bytesExtension(field.GetOptions(), ignoredField)
		dynamic, dynamicProblems := dynamicRules(field, message, schema.dynamic, supportedDynamicRules)
		if staticErr != nil {
			problems = append(problems, name+": "+staticErr.Error())
		}
		if ignoredErr != nil {
			problems = append(problems, name+": "+ignoredErr.Error())
		}
		for _, problem := range dynamicProblems {
			problems = append(problems, name+": "+problem)
		}

		hasStatic := false
		for _, value := range staticValues {
			hasStatic = hasStatic || len(value) != 0
		}
		hasRule := hasStatic || len(dynamic) != 0
		ignored := ""
		for _, value := range ignoredValues {
			ignored = string(value)
		}
		if len(ignoredValues) != 0 && ignored == "" {
			problems = append(problems, name+": field_coverage_ignored requires a reason")
		}
		if ignored != "" && hasRule {
			problems = append(problems, name+": cannot combine validation rules with field_coverage_ignored")
		}
		if ignored == "" && !hasRule {
			problem := name + ": add a validation rule or field_coverage_ignored reason"
			if suggestion := schema.suggestions[field.GetName()]; suggestion != "" && field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING {
				problem += "; consider " + suggestion
			}
			problems = append(problems, problem)
		}
	}
	return problems
}

func dynamicRules(field *descriptorpb.FieldDescriptorProto, message *descriptorpb.DescriptorProto, rules map[protowire.Number]dynamicRule, supported map[string]struct{}) ([]string, []string) {
	var enabled []string
	var problems []string
	for number, rule := range rules {
		values, err := varintExtension(field.GetOptions(), number)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, value := range values {
			if value == 0 {
				problems = append(problems, rule.name+" must be true when set")
				continue
			}
			if field.GetType() != rule.fieldType {
				problems = append(problems, fmt.Sprintf("%s requires %s, got %s", rule.name, rule.fieldType, field.GetType()))
				continue
			}
			if rule.messageType != "" && strings.TrimPrefix(field.GetTypeName(), ".") != rule.messageType {
				problems = append(problems, fmt.Sprintf("%s requires %s, got %s", rule.name, rule.messageType, field.GetTypeName()))
				continue
			}
			if rule.scope == 2 && !hasStringNamespace(message) {
				problems = append(problems, rule.name+" requires the request message to have a string namespace field")
				continue
			}
			if supported != nil {
				if _, ok := supported[rule.name]; !ok {
					problems = append(problems, rule.name+" has no registered Server implementation")
					continue
				}
			}
			enabled = append(enabled, rule.name)
		}
	}
	return enabled, problems
}

func hasStringNamespace(message *descriptorpb.DescriptorProto) bool {
	for _, field := range message.GetField() {
		if field.GetName() == "namespace" && field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING {
			return true
		}
	}
	return false
}

func buildIndex(set *descriptorpb.FileDescriptorSet) *index {
	result := &index{
		extensions:  make(map[string]protowire.Number),
		messages:    make(map[string]*descriptorpb.DescriptorProto),
		methods:     make(map[string]*descriptorpb.MethodDescriptorProto),
		methodFile:  make(map[string]string),
		suggestions: make(map[string]string),
		dynamic:     make(map[protowire.Number]dynamicRule),
	}
	if set == nil {
		return result
	}
	var dynamicExtensions []dynamicExtension
	for _, file := range set.GetFile() {
		prefix := file.GetPackage()
		for _, extension := range file.GetExtension() {
			name := joinName(prefix, extension.GetName())
			number := protowire.Number(extension.GetNumber())
			result.extensions[name] = number
			if strings.HasPrefix(name, dynamicRulePrefix) && extension.GetExtendee() == ".google.protobuf.FieldOptions" {
				dynamicExtensions = append(dynamicExtensions, dynamicExtension{name: name, descriptor: extension})
			}
			if extension.GetExtendee() == ".buf.validate.StringRules" {
				result.suggestions[extension.GetName()] = "(buf.validate.field).string.(" + name + ")"
			}
		}
		for _, message := range file.GetMessageType() {
			indexMessage(result.messages, "."+joinName(prefix, message.GetName()), message)
		}
		for _, service := range file.GetService() {
			serviceName := joinName(prefix, service.GetName())
			for _, method := range service.GetMethod() {
				name := joinName(serviceName, method.GetName())
				result.methods[name] = method
				result.methodFile[name] = file.GetName()
			}
		}
	}
	specNumber, hasSpec := result.extensions[ruleSpecName]
	for _, candidate := range dynamicExtensions {
		name := candidate.name
		extension := candidate.descriptor
		if extension.GetType() != descriptorpb.FieldDescriptorProto_TYPE_BOOL {
			result.problems = append(result.problems, name+": dynamic rule option must be a bool")
			continue
		}
		if !hasSpec {
			result.problems = append(result.problems, name+": validation schema does not define "+ruleSpecName)
			continue
		}
		values, err := bytesExtension(extension.GetOptions(), specNumber)
		if err != nil || len(values) != 1 {
			result.problems = append(result.problems, name+": dynamic rule must define one rule_spec")
			continue
		}
		rule, err := parseDynamicRuleSpec(name, values[0])
		if err != nil {
			result.problems = append(result.problems, name+": "+err.Error())
			continue
		}
		result.dynamic[protowire.Number(extension.GetNumber())] = rule
	}
	return result
}

func parseDynamicRuleSpec(name string, data []byte) (dynamicRule, error) {
	rule := dynamicRule{name: name}
	for len(data) != 0 {
		number, wireType, value, rest, err := consumeField(data)
		if err != nil {
			return dynamicRule{}, fmt.Errorf("invalid rule_spec: %w", err)
		}
		data = rest
		switch number {
		case 1:
			if wireType == protowire.VarintType {
				rule.scope = value.(uint64)
			}
		case 2:
			if wireType == protowire.VarintType {
				rule.fieldType = descriptorpb.FieldDescriptorProto_Type(value.(uint64))
			}
		case 3:
			if wireType == protowire.BytesType {
				rule.messageType = string(value.([]byte))
			}
		}
	}
	if rule.scope != 1 && rule.scope != 2 {
		return dynamicRule{}, errors.New("rule_spec requires a global or namespace scope")
	}
	if rule.fieldType == 0 {
		return dynamicRule{}, errors.New("rule_spec requires a field_type")
	}
	if rule.fieldType == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && rule.messageType == "" {
		return dynamicRule{}, errors.New("message rule_spec requires a message_type")
	}
	if rule.fieldType != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && rule.messageType != "" {
		return dynamicRule{}, errors.New("message_type is only valid for message fields")
	}
	if rule.scope == 1 && !strings.HasPrefix(name, "temporalvalidate.v1.dynamic_global_") {
		return dynamicRule{}, errors.New("global rule name must start with dynamic_global_")
	}
	if rule.scope == 2 && !strings.HasPrefix(name, "temporalvalidate.v1.dynamic_namespace_") {
		return dynamicRule{}, errors.New("namespace rule name must start with dynamic_namespace_")
	}
	return rule, nil
}

func indexMessage(messages map[string]*descriptorpb.DescriptorProto, name string, message *descriptorpb.DescriptorProto) {
	messages[name] = message
	for _, nested := range message.GetNestedType() {
		indexMessage(messages, name+"."+nested.GetName(), nested)
	}
}

func isTemporalAPIFile(name string) bool {
	return strings.HasPrefix(name, "temporal/api/")
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
