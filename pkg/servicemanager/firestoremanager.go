package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// FirestoreManager handles the creation, verification, and deletion of Firestore databases.
type FirestoreManager struct {
	client      DocumentStoreClient
	logger      zerolog.Logger
	environment Environment
}

// NewFirestoreManager creates a new manager for orchestrating Firestore resources.
func NewFirestoreManager(client DocumentStoreClient, logger zerolog.Logger, environment Environment) (*FirestoreManager, error) {
	if client == nil {
		return nil, errors.New("document store client (DocumentStoreClient interface) cannot be nil")
	}
	return &FirestoreManager{
		client:      client,
		logger:      logger.With().Str("component", "FirestoreManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates all configured Firestore databases. In GCP, a project can typically
// only have one Firestore database, so this process is simpler than for other resource types.
func (fm *FirestoreManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedFirestoreDatabase, error) {
	fm.logger.Info().Msg("Starting Firestore setup...")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.FirestoreDatabases))
	provisionedChan := make(chan ProvisionedFirestoreDatabase, len(resources.FirestoreDatabases))

	for _, dbCfg := range resources.FirestoreDatabases {
		wg.Add(1)
		go func(cfg FirestoreDatabase) {
			defer wg.Done()
			log := fm.logger.With().Str("database", cfg.Name).Logger()

			// In GCP, the Firestore database name is typically "(default)".
			// The `Name` field in the config is a logical name used for referencing.
			const gcpDefaultDbName = "(default)"
			dbHandle := fm.client.Database(gcpDefaultDbName)

			exists, err := dbHandle.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence for database '%s': %w", cfg.Name, err)
				return
			}

			if exists {
				log.Info().Msg("Firestore database already exists, skipping creation.")
				provisionedChan <- ProvisionedFirestoreDatabase{Name: cfg.Name}
				return
			}

			log.Info().Str("location", cfg.LocationID).Str("type", string(cfg.Type)).Msg("Creating Firestore database...")
			createErr := dbHandle.Create(ctx, cfg.LocationID, string(cfg.Type))
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create firestore database '%s': %w", cfg.Name, createErr)
			} else {
				log.Info().Msg("Firestore database created successfully.")
				provisionedChan <- ProvisionedFirestoreDatabase{Name: cfg.Name}
			}
		}(dbCfg)
	}

	wg.Wait()
	close(errChan)
	close(provisionedChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	var provisionedDBs []ProvisionedFirestoreDatabase
	for db := range provisionedChan {
		provisionedDBs = append(provisionedDBs, db)
	}

	if len(allErrors) > 0 {
		return provisionedDBs, errors.Join(allErrors...)
	}

	fm.logger.Info().Msg("Firestore setup completed successfully.")
	return provisionedDBs, nil
}

// Teardown skips deletion for Firestore databases, as this is a highly protected operation.
// It logs a warning to inform the user that manual deletion is required.
func (fm *FirestoreManager) Teardown(_ context.Context, resources CloudResourcesSpec) error {
	if len(resources.FirestoreDatabases) > 0 {
		fm.logger.Warn().Msg("Firestore teardown skipped. Deleting a Firestore database is a protected, high-impact operation that must be performed manually via the cloud console or gcloud CLI.")
	}
	return nil
}

// Verify checks if the configured Firestore databases exist.
func (fm *FirestoreManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	fm.logger.Info().Msg("Verifying Firestore databases...")
	var allErrors []error
	for _, dbCfg := range resources.FirestoreDatabases {
		const gcpDefaultDbName = "(default)"
		dbHandle := fm.client.Database(gcpDefaultDbName)

		exists, err := dbHandle.Exists(ctx)
		if err != nil {
			allErrors = append(allErrors, fmt.Errorf("failed to verify firestore database '%s': %w", dbCfg.Name, err))
			continue
		}

		if !exists {
			allErrors = append(allErrors, fmt.Errorf("firestore database '%s' not found", dbCfg.Name))
		}
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Firestore verification failed: %w", errors.Join(allErrors...))
	}

	fm.logger.Info().Msg("Firestore verification completed successfully.")
	return nil
}
