package machine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
)

var ErrLocalEnvironmentMissing = errors.New("local development environment image is missing")

func normalizeImmutableImageID(reference string) string {
	reference = strings.TrimSpace(reference)
	if isImmutableLocalImageID(reference) {
		return reference
	}
	if len(reference) == 64 {
		for _, character := range reference {
			if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
				return reference
			}
		}
		return "sha256:" + reference
	}
	return reference
}

func isImmutableLocalImageID(reference string) bool {
	reference = strings.TrimSpace(reference)
	return strings.HasPrefix(reference, "sha256:") && len(reference) > len("sha256:")
}

func missingImageError(reference string, pullImages bool) error {
	if isImmutableLocalImageID(reference) {
		return fmt.Errorf("%w: %s", ErrLocalEnvironmentMissing, reference)
	}
	if !pullImages {
		return fmt.Errorf("image %s is not present and pulling is disabled", reference)
	}
	return nil
}

func environmentImmutableImageID(environment *domain.DevMachineEnvironment) string {
	if environment == nil {
		return ""
	}
	if environment.ImageDigest != nil && isImmutableLocalImageID(*environment.ImageDigest) {
		return strings.TrimSpace(*environment.ImageDigest)
	}
	if isImmutableLocalImageID(environment.ImageRef) {
		return strings.TrimSpace(environment.ImageRef)
	}
	return ""
}

func environmentImageCleanupReference(environment *domain.DevMachineEnvironment) string {
	if reference := environmentImmutableImageID(environment); reference != "" {
		return reference
	}
	if environment == nil {
		return ""
	}
	return strings.TrimSpace(environment.ImageRef)
}

func validateEnvironmentImageLabels(labels map[string]string, workspaceID, environmentID uuid.UUID) error {
	if labels == nil {
		return fmt.Errorf("missing image labels")
	}
	if labels["com.strifira.managed"] != "true" {
		return fmt.Errorf("missing Strifira management label")
	}
	if labels["com.strifira.kind"] != "dev-machine-environment" {
		return fmt.Errorf("not a Strifira development environment image")
	}
	if labels["com.strifira.environment-id"] != environmentID.String() {
		return fmt.Errorf("environment label mismatch")
	}
	if labels["com.strifira.workspace-id"] != workspaceID.String() {
		return fmt.Errorf("workspace label mismatch")
	}
	return nil
}
