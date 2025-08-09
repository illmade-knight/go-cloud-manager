package prerequisites

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// Manager orchestrates the process of checking and enabling required cloud APIs.
type Manager struct {
	client  ServiceAPIClient
	planner *PrerequisitePlanner
	logger  zerolog.Logger
}

// NewManager creates a new prerequisite manager.
func NewManager(client ServiceAPIClient, logger zerolog.Logger) *Manager {
	return &Manager{
		client:  client,
		planner: NewPlanner(),
		logger:  logger.With().Str("component", "PrerequisiteManager").Logger(),
	}
}

// CheckAndEnable analyzes the architecture, checks for missing APIs, and enables them.
// This function is idempotent.
func (m *Manager) CheckAndEnable(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) error {
	m.logger.Info().Msg("Planning and verifying service API prerequisites...")

	// 1. Plan which services are required based on the architecture.
	requiredServices := m.planner.PlanRequiredServices(arch)
	if len(requiredServices) == 0 {
		m.logger.Info().Msg("No specific cloud services are required by the architecture. Skipping checks.")
		return nil
	}
	m.logger.Info().Strs("required_apis", requiredServices).Msg("Planned required APIs from architecture.")

	// 2. Get the list of currently enabled services.
	enabledServices, err := m.client.GetEnabledServices(ctx, arch.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get currently enabled services: %w", err)
	}

	// 3. Compare the lists to find what needs to be enabled.
	var servicesToEnable []string
	for _, required := range requiredServices {
		if _, ok := enabledServices[required]; !ok {
			servicesToEnable = append(servicesToEnable, required)
		}
	}

	// 4. If there's anything to enable, call the client.
	if len(servicesToEnable) > 0 {
		m.logger.Warn().Strs("apis_to_enable", servicesToEnable).Msg("Found required APIs that are not yet enabled. Attempting to enable now...")
		err = m.client.EnableServices(ctx, arch.ProjectID, servicesToEnable)
		if err != nil {
			return fmt.Errorf("failed to enable required services: %w", err)
		}
		m.logger.Info().Strs("enabled_apis", servicesToEnable).Msg("✅ Successfully enabled all required service APIs.")
	} else {
		m.logger.Info().Msg("✅ All required service APIs are already enabled.")
	}

	return nil
}
