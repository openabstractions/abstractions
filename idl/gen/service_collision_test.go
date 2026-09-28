package main

import (
	"strings"
	"testing"
)

func TestServiceRejectsCodecHelperCollision(t *testing.T) {
	for _, name := range []string{"Reader", "Raw", "Refusal"} {
		t.Run(name, func(t *testing.T) {
			_, err := parse(head + strings.Replace(serviceFixture, "service Events", "service "+name, 1))
			if err == nil || !strings.Contains(err.Error(), "name collision") {
				t.Fatalf("got %v; expected helper collision", err)
			}
		})
	}
}

func TestServiceRejectsDescribeMetadataHookCollision(t *testing.T) {
	for _, method := range []string{"DescribeMetadata", "describeMetadata", "describe_metadata", "DescribeServiceMetadata", "describe_service_metadata"} {
		t.Run(method, func(t *testing.T) {
			_, err := parse(head + strings.Replace(replyFixture, "Record Echo", "string "+method+"() Record Echo", 1))
			if err == nil || !strings.Contains(err.Error(), "colliding method") {
				t.Fatalf("got %v; expected metadata hook collision", err)
			}
		})
	}
}
