package servicedirector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/microservice"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// Command represents the structure of a message from the orchestrator.
type Command struct {
	Name         string `json:"command"` // e.g., "setup", "teardown"
	DataflowName string `json:"dataflow_name"`
}

// Director implements the builder.Service interface for our main application.
type Director struct {
	*microservice.BaseServer
	serviceManager *servicemanager.ServiceManager
	architecture   *servicemanager.MicroserviceArchitecture
	config         *Config
	logger         zerolog.Logger
	pubsubClient   *pubsub.Client
}

// NewServiceDirector creates and initializes a new Director instance.
func NewServiceDirector(ctx context.Context, cfg *Config, loader servicemanager.ArchitectureIO, schemaRegistry map[string]interface{}, psClient *pubsub.Client, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()

	arch, err := loader.LoadArchitecture(ctx)
	if err != nil {
		return nil, fmt.Errorf("director: failed to load service definitions: %w", err)
	}
	logger.Info().Str("projectID", arch.ProjectID).Msg("loaded project architecture")

	sm, err := servicemanager.NewServiceManager(ctx, arch.Environment, schemaRegistry, nil, directorLogger)
	if err != nil {
		return nil, fmt.Errorf("director: failed to create ServiceManager: %w", err)
	}

	baseServer := microservice.NewBaseServer(directorLogger, cfg.HTTPPort)

	d := &Director{
		BaseServer:     baseServer,
		serviceManager: sm,
		architecture:   arch,
		config:         cfg,
		logger:         directorLogger,
		pubsubClient:   psClient,
	}

	mux := baseServer.Mux()
	mux.HandleFunc("/orchestrate/setup", d.setupHandler)
	mux.HandleFunc("/orchestrate/teardown", d.teardownHandler)
	mux.HandleFunc("/verify/dataflow", d.verifyHandler)

	// Start the command listener in a background goroutine.
	go d.listenForCommands(ctx)

	directorLogger.Info().
		Str("http_port", cfg.HTTPPort).
		Str("command_topic", cfg.CommandTopic).
		Msg("Director initialized and listening for commands.")

	return d, nil
}

func (d *Director) handleSetupCommand(ctx context.Context, dataflowName string) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'setup' command...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if dataflowName == "all" {
		_, err := d.serviceManager.SetupAll(ctx, d.architecture)
		return err
	}
	_, err := d.serviceManager.SetupDataflow(ctx, d.architecture, dataflowName)
	return err
}

func (d *Director) handleTeardownCommand(ctx context.Context, dataflowName string) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'teardown' command...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if dataflowName == "all" {
		return d.serviceManager.TeardownAll(ctx, d.architecture)
	}
	return d.serviceManager.TeardownDataflow(ctx, d.architecture, dataflowName)
}

func (d *Director) listenForCommands(ctx context.Context) {
	sub := d.pubsubClient.Subscription(d.config.CommandSubscription)
	d.logger.Info().Str("subscription", sub.String()).Msg("Director command listener started.")

	err := sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		d.logger.Info().Str("message_id", msg.ID).Msg("Received command message")

		var cmd Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			d.logger.Error().Err(err).Msg("Failed to unmarshal command message")
			return
		}

		switch cmd.Name {
		case "setup":
			if err := d.handleSetupCommand(ctx, cmd.DataflowName); err != nil {
				d.logger.Error().Err(err).Str("dataflow", cmd.DataflowName).Msg("Setup command failed")
			}
		case "teardown":
			if err := d.handleTeardownCommand(ctx, cmd.DataflowName); err != nil {
				d.logger.Error().Err(err).Str("dataflow", cmd.DataflowName).Msg("Teardown command failed")
			}
		default:
			d.logger.Warn().Str("command", cmd.Name).Msg("Received unknown command")
		}
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		d.logger.Error().Err(err).Msg("Command listener shut down with error")
	} else {
		d.logger.Info().Msg("Command listener shut down gracefully.")
	}
}

// Shutdown now also closes the Pub/Sub client.
func (d *Director) Shutdown() {
	d.BaseServer.Shutdown()
	if d.pubsubClient != nil {
		d.pubsubClient.Close()
	}
}

// setupHandler triggers a setup for all dataflows.
// NOTE: In automated pipelines, this logic is triggered via an asynchronous
// Pub/Sub message. This synchronous HTTP endpoint is retained for local
// development, manual testing, or use within a trusted network.
func (d *Director) setupHandler(w http.ResponseWriter, r *http.Request) {
	if err := d.handleSetupCommand(r.Context(), "all"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to setup all dataflows: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("All dataflows setup successfully triggered."))
}

// teardownHandler triggers a teardown for all ephemeral dataflows.
// NOTE: In automated pipelines, this logic is triggered via an asynchronous
// Pub/Sub message. This synchronous HTTP endpoint is retained for local
// development, manual testing, or use within a trusted network.
func (d *Director) teardownHandler(w http.ResponseWriter, r *http.Request) {
	if err := d.handleTeardownCommand(r.Context(), "all"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to teardown all dataflows: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("All ephemeral dataflows teardown successfully triggered."))
}

func (d *Director) verifyHandler(w http.ResponseWriter, r *http.Request) {
	var req VerifyDataflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	d.logger.Info().Str("dataflow", req.DataflowName).Str("service", req.ServiceName).Msg("Received request to verify dataflow.")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	err := d.serviceManager.VerifyDataflow(ctx, d.architecture, req.DataflowName)
	if err != nil {
		d.logger.Error().Err(err).Str("dataflow", req.DataflowName).Msg("Dataflow verification failed")
		http.Error(w, fmt.Sprintf("Dataflow '%s' verification failed: %v", req.DataflowName, err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("Dataflow '%s' verified successfully.", req.DataflowName)))
}
