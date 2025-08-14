package main

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test simulates the pre-flight check for the publisher service.
// It ensures that the loadAndValidateConfig function can correctly load and
// parse a given resources.yaml configuration.
func TestConfigLoading(t *testing.T) {
	// --- Arrange ---
	// Create a dummy resources.yaml file content for the test.
	const testProjectID = "test-project-id"

	yamlContent := `
  topics:
    - name: test-topic-123
`
	yamlBytes, err := os.ReadFile("resources.yaml")
	if err != nil {
		// If the file doesn't exist, create dummy content for a standalone run.
		if errors.Is(err, os.ErrNotExist) {
			yamlBytes = []byte(yamlContent)
		} else {
			// For any other read error, fail the test.
			require.NoError(t, err, "Failed to read resources.yaml")
		}
	} else {
		t.Log("Using existing resources.yaml for validation.")
	}

	// Set the required environment variable.
	t.Setenv("PROJECT_ID", testProjectID)

	// --- Act ---
	// Call the actual loadAndValidateConfig function from main.go.
	cfg, err := loadAndValidateConfig(yamlBytes)

	// --- Assert ---
	// Verify that the configuration was loaded correctly.
	require.NoError(t, err)
	require.NotEmpty(t, cfg.TopicID, "TopicID should be loaded from the YAML file")
	require.Equal(t, testProjectID, cfg.ProjectID, "ProjectID should be loaded from the environment")
}
