package main

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test simulates the pre-flight check for the publisher service.
func TestConfigLoading(t *testing.T) {
	// --- Arrange ---
	const testProjectID = "test-project-id"

	// REFACTOR: Create a dummy tracer-config.yaml for the test run.
	dummyTracerConfig := "test_parameter: \"hello from preflight test\""
	err := os.WriteFile("tracer-config.yaml", []byte(dummyTracerConfig), 0644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove("tracer-config.yaml") })

	resourcesContent := `
topics:
  - name: test-topic-123
    producer_service:
      name: tracer-publisher-123
      lookup:
          key: tracer-topic-id
          method: yaml
`
	resourcesBytes, err := os.ReadFile("resources.yaml")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resourcesBytes = []byte(resourcesContent)
		} else {
			require.NoError(t, err, "Failed to read resources.yaml")
		}
	}

	tracerBytes, err := os.ReadFile("tracer-config.yaml")
	require.NoError(t, err)

	t.Setenv("PROJECT_ID", testProjectID)

	// --- Act ---
	cfg, err := loadAndValidateConfig(resourcesBytes, tracerBytes)

	// --- Assert ---
	require.NoError(t, err)
	require.NotEmpty(t, cfg.TopicID, "TopicID should be loaded from the YAML file")
	require.Equal(t, testProjectID, cfg.ProjectID, "ProjectID should be loaded from the environment")
	require.Equal(t, "hello from preflight test", cfg.TestParameter, "TestParameter should be loaded from tracer-config.yaml")
}
