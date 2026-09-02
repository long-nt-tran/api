package main

import (
	"os"
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
