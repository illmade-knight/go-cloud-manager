package docker

import (
	"fmt"
	"github.com/rs/zerolog"
	"os"
	"path/filepath"
	"text/template"
)

// dockerfileData holds the variables for populating the Dockerfile template.
type dockerfileData struct {
	ServiceName     string
	MainPackagePath string
}

type FileWriter struct {
	logger                zerolog.Logger
	mainPackagePath       string
	defaultDockerRegistry string
}

func NewFileWriter(logger zerolog.Logger, mainPackagePath, defaultDockerRegistry string) *FileWriter {
	return &FileWriter{
		logger:                logger,
		mainPackagePath:       mainPackagePath,
		defaultDockerRegistry: defaultDockerRegistry,
	}
}

func (b *FileWriter) DockerfileExists(dockerfilePath string) (bool, error) {
	if _, err := os.Stat(dockerfilePath); err == nil {
		b.logger.Info().Str("path", dockerfilePath).Msg("Dockerfile already exists, skipping generation.")
		return true, nil
	} else if !os.IsNotExist(err) {
		// If os.Stat returns an error other than "not found", it's a problem.
		return false, fmt.Errorf("failed to check for existing Dockerfile at %s: %w", dockerfilePath, err)
	}
	return false, nil
}

// GenerateAndWriteDockerfile creates a standard Go Dockerfile from a template if one doesn't already exist.
func (b *FileWriter) GenerateAndWriteDockerfile(serviceName, dockerfilePath string) error {
	// 1. Parse the template string defined in docker/template.go
	tmpl, err := template.New("dockerfile").Parse(goDockerfileTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse dockerfile template: %w", err)
	}
	b.logger.Info().Str("path", dockerfilePath).Msg("initializing Dockerfile")
	// 2. Create the data object to pass to the template.
	data := dockerfileData{
		ServiceName:     serviceName,
		MainPackagePath: b.mainPackagePath,
	}

	// Ensure the directory for the Dockerfile exists.
	dir := filepath.Dir(dockerfilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for Dockerfile %s: %w", dir, err)
	}
	b.logger.Info().Str("directory", dir).Msg("directory exists")

	// 3. Create the actual Dockerfile on disk.
	file, err := os.Create(dockerfilePath)
	if err != nil {
		return fmt.Errorf("failed to create Dockerfile at %s: %w", dockerfilePath, err)
	}
	defer file.Close()
	b.logger.Info().Str("path", dockerfilePath).Msg("created Dockerfile")

	// 4. Execute the template, writing the generated content directly to the file.
	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute dockerfile template for %s: %w", serviceName, err)
	}

	b.logger.Info().Str("path", dockerfilePath).Msg("Successfully generated and wrote Dockerfile.")
	return nil
}
