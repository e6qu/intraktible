// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/modeling/domain"
)

type modelingConnectionFlags struct {
	serverURL *string
	apiKey    *string
}

func bindModelingConnection(fs *flag.FlagSet) modelingConnectionFlags {
	return modelingConnectionFlags{
		serverURL: fs.String("server", "http://localhost:8080", "intraktible server URL"),
		apiKey:    fs.String("api-key", os.Getenv("INTRAKTIBLE_API_KEY"), "API key"),
	}
}

func (flags modelingConnectionFlags) client() *client.Client {
	return client.New(*flags.serverURL, *flags.apiKey)
}

func modelingCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"modeling: command required (schemas|define-schema|request-schema-approval|" +
				"decide-schema|retire-schema|quality|acknowledge-quality|resolve-quality|health|features|datasets|" +
				"define-dataset|snapshots|snapshot|rows|export-snapshot|jobs|pause-job|resume-job|retry-job|cancel-job|train|" +
				"evaluations|evaluate|artifacts|register-artifact|stage-artifact|verify-artifact|backfills|backfill|" +
				"materializations|models|validate-model|request-model-approval|" +
				"decide-model|retire-model|lineage|compare)",
		)
	}
	switch args[0] {
	case "schemas":
		return modelingSchemas(args[1:])
	case "define-schema":
		return modelingFromFile(
			"modeling define-schema", "schema definition JSON file", args[1:],
			func(c *client.Client, value domain.SchemaSpec) (any, error) {
				return c.DefineSchema(context.Background(), value)
			},
		)
	case "request-schema-approval":
		return modelingRequestSchemaApproval(args[1:])
	case "decide-schema":
		return modelingDecideSchema(args[1:])
	case "retire-schema":
		return modelingRetireSchema(args[1:])
	case "quality":
		return modelingQuality(args[1:])
	case "acknowledge-quality":
		return modelingAcknowledgeQuality(args[1:])
	case "resolve-quality":
		return modelingResolveQuality(args[1:])
	case "health":
		return modelingHealth(args[1:])
	case "features":
		return modelingFeatures(args[1:])
	case "datasets":
		return modelingDatasets(args[1:])
	case "define-dataset":
		return modelingFromFile(
			"modeling define-dataset", "dataset definition JSON file", args[1:],
			func(c *client.Client, value domain.DatasetSpec) (any, error) {
				return c.DefineDataset(context.Background(), value)
			},
		)
	case "snapshots":
		return modelingSnapshots(args[1:])
	case "snapshot":
		return modelingSnapshot(args[1:])
	case "rows":
		return modelingRows(args[1:])
	case "export-snapshot":
		return modelingExportSnapshot(args[1:])
	case "jobs":
		return modelingJobs(args[1:])
	case "pause-job":
		return modelingJobTransition(args[1:], "pause")
	case "resume-job":
		return modelingJobTransition(args[1:], "resume")
	case "retry-job":
		return modelingJobTransition(args[1:], "retry")
	case "cancel-job":
		return modelingCancelJob(args[1:])
	case "train":
		return modelingFromFile(
			"modeling train", "training request JSON file", args[1:],
			func(c *client.Client, value domain.TrainingRequest) (any, error) {
				return c.RequestTraining(context.Background(), value)
			},
		)
	case "evaluations":
		return modelingEvaluations(args[1:])
	case "evaluate":
		return modelingFromFile(
			"modeling evaluate", "evaluation request JSON file", args[1:],
			func(c *client.Client, value domain.EvaluationRequest) (any, error) {
				return c.RequestEvaluation(context.Background(), value)
			},
		)
	case "artifacts":
		return modelingArtifacts(args[1:])
	case "register-artifact":
		return modelingFromFile(
			"modeling register-artifact", "external artifact registration JSON file", args[1:],
			func(c *client.Client, value domain.ArtifactRegistration) (any, error) {
				return c.RegisterExternalArtifact(context.Background(), value)
			},
		)
	case "stage-artifact":
		return modelingStageArtifact(args[1:])
	case "verify-artifact":
		return modelingVerifyArtifact(args[1:])
	case "backfills", "backfill":
		return modelingFromFile(
			"modeling backfill", "backfill request JSON file", args[1:],
			func(c *client.Client, value domain.BackfillRequest) (any, error) {
				return c.RequestBackfill(context.Background(), value)
			},
		)
	case "materializations":
		return modelingMaterializations(args[1:])
	case "models":
		return modelingModels(args[1:])
	case "validate-model":
		return modelingValidateModel(args[1:])
	case "request-model-approval":
		return modelingRequestModelApproval(args[1:])
	case "decide-model":
		return modelingDecideModel(args[1:])
	case "retire-model":
		return modelingRetireModel(args[1:])
	case "lineage":
		return modelingLineage(args[1:])
	case "compare":
		return modelingCompare(args[1:])
	default:
		return fmt.Errorf("modeling: unknown command %q", args[0])
	}
}

func modelingStageArtifact(args []string) error {
	fs := flag.NewFlagSet("modeling stage-artifact", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	artifactID := fs.String("artifact", "", "artifact id")
	stage := fs.String("stage", "", "target stage: validated, production, or archived")
	reason := fs.String("reason", "", "stage change evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := domain.ArtifactStage(*stage)
	if *artifactID == "" || strings.TrimSpace(*reason) == "" ||
		(target != domain.ArtifactValidated &&
			target != domain.ArtifactProduction &&
			target != domain.ArtifactArchived) {
		return errors.New(
			"modeling stage-artifact: --artifact, --stage validated|production|archived, and --reason are required",
		)
	}
	result, err := connection.client().ChangeArtifactStage(
		context.Background(), *artifactID, target, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingFromFile[T any](
	command string,
	description string,
	args []string,
	run func(*client.Client, T) (any, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	file := fs.String("file", "", description)
	if err := fs.Parse(args); err != nil {
		return err
	}
	value, err := readAgentJSON[T](*file)
	if err != nil {
		return err
	}
	result, err := run(connection.client(), value)
	if err != nil {
		return err
	}
	return printJSON(result)
}

// modelingListOrGet implements the shared read shape: an optional id flag gets
// one record, otherwise the command lists them all.
func modelingListOrGet(
	args []string,
	command string,
	flagName string,
	flagHelp string,
	get func(*client.Client, string) (any, error),
	list func(*client.Client) (any, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	id := fs.String(flagName, "", flagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id != "" {
		item, err := get(connection.client(), *id)
		if err != nil {
			return err
		}
		return printJSON(item)
	}
	items, err := list(connection.client())
	if err != nil {
		return err
	}
	return printJSON(items)
}

// modelingTwoFlagAction implements the shared mutation shape: a required id
// flag plus required free-text evidence, then one client call.
func modelingTwoFlagAction(
	args []string,
	command string,
	firstFlag string,
	firstHelp string,
	secondFlag string,
	secondHelp string,
	emit func(any) error,
	call func(*client.Client, string, string) (any, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	first := fs.String(firstFlag, "", firstHelp)
	second := fs.String(secondFlag, "", secondHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *first == "" || strings.TrimSpace(*second) == "" {
		return fmt.Errorf("%s: --%s and --%s are required", command, firstFlag, secondFlag)
	}
	result, err := call(connection.client(), *first, *second)
	if err != nil {
		return err
	}
	return emit(result)
}

func bindSchemaRef(fs *flag.FlagSet) (*string, *string, *string) {
	return fs.String("kind", "", "schema kind: entity | event"),
		fs.String("entity-type", "", "entity type"),
		fs.String("event-name", "", "event name (required for event schema)")
}

func schemaRef(kind, entityType, eventName string) (domain.SchemaRef, error) {
	ref := domain.SchemaRef{
		Kind: domain.SchemaKind(kind), EntityType: entityType, EventName: eventName,
	}
	return ref, ref.Validate()
}

func modelingSchemas(args []string) error {
	fs := flag.NewFlagSet("modeling schemas", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	kind, entityType, eventName := bindSchemaRef(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kind == "" && *entityType == "" && *eventName == "" {
		items, err := connection.client().ListSchemas(context.Background())
		if err != nil {
			return err
		}
		return printJSON(items)
	}
	ref, err := schemaRef(*kind, *entityType, *eventName)
	if err != nil {
		return err
	}
	item, err := connection.client().GetSchema(context.Background(), ref)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func modelingRequestSchemaApproval(args []string) error {
	fs := flag.NewFlagSet("modeling request-schema-approval", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	kind, entityType, eventName := bindSchemaRef(fs)
	version := fs.Int("version", 0, "exact schema version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := schemaRef(*kind, *entityType, *eventName)
	if err != nil {
		return err
	}
	if *version <= 0 {
		return errors.New("modeling request-schema-approval: positive --version is required")
	}
	requestID, err := connection.client().RequestSchemaApproval(
		context.Background(), ref, *version,
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"request_id": requestID})
}

func modelingDecideSchema(args []string) error {
	fs := flag.NewFlagSet("modeling decide-schema", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	kind, entityType, eventName := bindSchemaRef(fs)
	requestID := fs.String("request", "", "approval request id")
	approve := fs.Bool("approve", false, "approve the requested version")
	reason := fs.String("reason", "", "checker reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := schemaRef(*kind, *entityType, *eventName)
	if err != nil {
		return err
	}
	if *requestID == "" {
		return errors.New("modeling decide-schema: --request is required")
	}
	result, err := connection.client().DecideSchemaApproval(
		context.Background(), *requestID, ref, *approve, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingRetireSchema(args []string) error {
	fs := flag.NewFlagSet("modeling retire-schema", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	kind, entityType, eventName := bindSchemaRef(fs)
	version := fs.Int("version", 0, "exact schema version")
	reason := fs.String("reason", "", "retirement reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := schemaRef(*kind, *entityType, *eventName)
	if err != nil {
		return err
	}
	if *version <= 0 || strings.TrimSpace(*reason) == "" {
		return errors.New("modeling retire-schema: positive --version and --reason are required")
	}
	result, err := connection.client().RetireSchema(
		context.Background(), ref, *version, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingQuality(args []string) error {
	fs := flag.NewFlagSet("modeling quality", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	observations := fs.Bool("observations", false, "list all observations instead of incidents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *observations {
		items, err := connection.client().ListQualityObservations(context.Background())
		if err != nil {
			return err
		}
		return printJSON(items)
	}
	items, err := connection.client().ListQualityIncidents(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func modelingAcknowledgeQuality(args []string) error {
	return modelingTwoFlagAction(
		args, "modeling acknowledge-quality",
		"incident", "quality incident id", "note", "ownership and triage note",
		printJSON, func(c *client.Client, incidentID, note string) (any, error) {
			return c.AcknowledgeQualityIncident(context.Background(), incidentID, note)
		},
	)
}

func modelingResolveQuality(args []string) error {
	return modelingTwoFlagAction(
		args, "modeling resolve-quality",
		"incident", "quality incident id", "reason", "resolution evidence",
		printJSON, func(c *client.Client, incidentID, reason string) (any, error) {
			return c.ResolveQualityIncident(context.Background(), incidentID, reason)
		},
	)
}

func modelingHealth(args []string) error {
	fs := flag.NewFlagSet("modeling health", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListSourceHealth(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func modelingFeatures(args []string) error {
	fs := flag.NewFlagSet("modeling features", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListGovernedFeatures(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func modelingDatasets(args []string) error {
	return modelingListOrGet(
		args, "modeling datasets", "name", "optional dataset name",
		func(c *client.Client, name string) (any, error) {
			return c.GetDataset(context.Background(), name)
		},
		func(c *client.Client) (any, error) {
			return c.ListDatasets(context.Background())
		},
	)
}

func modelingSnapshots(args []string) error {
	return modelingListOrGet(
		args, "modeling snapshots", "snapshot", "optional snapshot id",
		func(c *client.Client, snapshotID string) (any, error) {
			return c.GetSnapshot(context.Background(), snapshotID)
		},
		func(c *client.Client) (any, error) {
			return c.ListSnapshots(context.Background())
		},
	)
}

func modelingSnapshot(args []string) error {
	return modelingFromFile(
		"modeling snapshot", "snapshot request JSON file", args,
		func(c *client.Client, value domain.SnapshotRequest) (any, error) {
			return c.RequestSnapshot(
				context.Background(), value.DatasetName, value.Version, value,
			)
		},
	)
}

func modelingRows(args []string) error {
	fs := flag.NewFlagSet("modeling rows", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	snapshotID := fs.String("snapshot", "", "snapshot id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *snapshotID == "" {
		return errors.New("modeling rows: --snapshot is required")
	}
	rows, err := connection.client().SnapshotRows(context.Background(), *snapshotID)
	if err != nil {
		return err
	}
	return printJSON(rows)
}

func modelingExportSnapshot(args []string) error {
	fs := flag.NewFlagSet("modeling export-snapshot", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	snapshotID := fs.String("snapshot", "", "snapshot id")
	format := fs.String("format", "json", "export format: json or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *snapshotID == "" || (*format != "json" && *format != "csv") {
		return errors.New(
			"modeling export-snapshot: --snapshot and --format json|csv are required",
		)
	}
	payload, err := connection.client().ExportSnapshot(
		context.Background(), *snapshotID, *format,
	)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(payload)
	return err
}

func modelingJobs(args []string) error {
	return modelingListOrGet(
		args, "modeling jobs", "job", "optional job id",
		func(c *client.Client, jobID string) (any, error) {
			return c.GetModelingJob(context.Background(), jobID)
		},
		func(c *client.Client) (any, error) {
			return c.ListModelingJobs(context.Background())
		},
	)
}

func modelingCancelJob(args []string) error {
	return modelingTwoFlagAction(
		args, "modeling cancel-job",
		"job", "job id", "reason", "cancellation reason",
		printJSON, func(c *client.Client, jobID, reason string) (any, error) {
			return c.CancelModelingJob(context.Background(), jobID, reason)
		},
	)
}

func modelingJobTransition(args []string, transition string) error {
	fs := flag.NewFlagSet("modeling "+transition+"-job", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	jobID := fs.String("job", "", "job id")
	reason := fs.String("reason", "", transition+" reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *jobID == "" || strings.TrimSpace(*reason) == "" {
		return fmt.Errorf(
			"modeling %s-job: --job and --reason are required", transition,
		)
	}
	var (
		result client.CommandResult
		err    error
	)
	switch transition {
	case "pause":
		result, err = connection.client().PauseModelingJob(
			context.Background(), *jobID, *reason,
		)
	case "resume":
		result, err = connection.client().ResumeModelingJob(
			context.Background(), *jobID, *reason,
		)
	case "retry":
		result, err = connection.client().RetryModelingJob(
			context.Background(), *jobID, *reason,
		)
	default:
		return fmt.Errorf("modeling: unsupported job transition %q", transition)
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingEvaluations(args []string) error {
	return modelingListOrGet(
		args, "modeling evaluations", "evaluation", "optional evaluation id",
		func(c *client.Client, evaluationID string) (any, error) {
			return c.GetEvaluation(context.Background(), evaluationID)
		},
		func(c *client.Client) (any, error) {
			return c.ListEvaluations(context.Background())
		},
	)
}

func modelingArtifacts(args []string) error {
	return modelingListOrGet(
		args, "modeling artifacts", "artifact", "optional artifact id",
		func(c *client.Client, artifactID string) (any, error) {
			return c.GetArtifact(context.Background(), artifactID)
		},
		func(c *client.Client) (any, error) {
			return c.ListArtifacts(context.Background())
		},
	)
}

func modelingVerifyArtifact(args []string) error {
	fs := flag.NewFlagSet("modeling verify-artifact", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	artifactID := fs.String("artifact", "", "artifact id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *artifactID == "" {
		return errors.New("modeling verify-artifact: --artifact is required")
	}
	if err := connection.client().VerifyArtifact(context.Background(), *artifactID); err != nil {
		return err
	}
	return printJSON(map[string]bool{"valid": true})
}

func modelingMaterializations(args []string) error {
	return modelingListOrGet(
		args, "modeling materializations", "backfill", "optional backfill id",
		func(c *client.Client, backfillID string) (any, error) {
			return c.GetMaterialization(context.Background(), backfillID)
		},
		func(c *client.Client) (any, error) {
			return c.ListMaterializations(context.Background())
		},
	)
}

func modelingModels(args []string) error {
	return modelingListOrGet(
		args, "modeling models", "model", "optional model name",
		func(c *client.Client, name string) (any, error) {
			return c.GetModel(context.Background(), name)
		},
		func(c *client.Client) (any, error) {
			return c.ListModels(context.Background())
		},
	)
}

func modelingValidateModel(args []string) error {
	fs := flag.NewFlagSet("modeling validate-model", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	name := fs.String("model", "", "model name")
	file := fs.String("file", "", "validation evidence JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("modeling validate-model: --model is required")
	}
	request, err := readAgentJSON[client.ModelValidationRequest](*file)
	if err != nil {
		return err
	}
	result, err := connection.client().RecordModelValidation(
		context.Background(), *name, request,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingRequestModelApproval(args []string) error {
	fs := flag.NewFlagSet("modeling request-model-approval", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	name := fs.String("model", "", "model name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("modeling request-model-approval: --model is required")
	}
	requestID, err := connection.client().RequestModelApproval(context.Background(), *name)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"request_id": requestID})
}

func modelingDecideModel(args []string) error {
	fs := flag.NewFlagSet("modeling decide-model", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	name := fs.String("model", "", "model name")
	requestID := fs.String("request", "", "approval request id")
	approve := fs.Bool("approve", false, "approve the requested version")
	reason := fs.String("reason", "", "checker reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*requestID) == "" {
		return errors.New("modeling decide-model: --model and --request are required")
	}
	result, err := connection.client().DecideModelApproval(
		context.Background(), *name, *requestID, *approve, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingRetireModel(args []string) error {
	fs := flag.NewFlagSet("modeling retire-model", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	name := fs.String("model", "", "model name")
	reason := fs.String("reason", "", "retirement reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("modeling retire-model: --model and --reason are required")
	}
	result, err := connection.client().RetireModel(context.Background(), *name, *reason)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func modelingLineage(args []string) error {
	fs := flag.NewFlagSet("modeling lineage", flag.ContinueOnError)
	connection := bindModelingConnection(fs)
	model := fs.String("model", "", "model name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return errors.New("modeling lineage: --model is required")
	}
	result, err := connection.client().ModelLineage(context.Background(), *model)
	if err != nil {
		return err
	}
	return printRawJSON(result)
}

func modelingCompare(args []string) error {
	return modelingTwoFlagAction(
		args, "modeling compare",
		"champion", "champion model name", "challenger", "challenger model name",
		func(value any) error {
			raw, ok := value.(json.RawMessage)
			if !ok {
				return fmt.Errorf("modeling compare: unexpected result %T", value)
			}
			return printRawJSON(raw)
		},
		func(c *client.Client, champion, challenger string) (any, error) {
			return c.CompareModels(context.Background(), champion, challenger)
		},
	)
}
