package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestREADMEExampleMatchesFixture(t *testing.T) {
	readme, err := os.ReadFile("../../temporalvalidate/README.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("../../temporalvalidate/examples/v1/example.proto")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "```protobuf\n"+string(example)+"```\n") {
		t.Fatal("README example differs from temporalvalidate/examples/v1/example.proto")
	}
}

func TestREADMEDynamicDeclarationsMatchSchema(t *testing.T) {
	readme, err := os.ReadFile("../../temporalvalidate/README.md")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile("../../temporalvalidate/v1/rules.proto")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"dynamic_global_max_id_length",
		"dynamic_namespace_max_reason_length",
		"dynamic_namespace_max_payload_size",
	} {
		t.Run(name, func(t *testing.T) {
			pattern := regexp.MustCompile(`(?s)  optional bool ` + regexp.QuoteMeta(name) + ` = \d+ \[\(rule_spec\) = \{.*?\}\];`)
			declaration := pattern.FindString(string(rules))
			if declaration == "" {
				t.Fatal("dynamic option declaration not found in rules.proto")
			}
			if !strings.Contains(string(readme), declaration) {
				t.Fatal("README dynamic option declaration differs from rules.proto")
			}
		})
	}
}

func TestREADMECanonicalDeclarationMatchesSchema(t *testing.T) {
	readme, err := os.ReadFile("../../temporalvalidate/README.md")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile("../../temporalvalidate/v1/rules.proto")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)  optional bool namespace = \d+ \[.*?\n  \];`)
	declaration := pattern.FindString(string(rules))
	if declaration == "" || !strings.Contains(string(readme), declaration) {
		t.Fatal("README canonical declaration differs from rules.proto")
	}
}
