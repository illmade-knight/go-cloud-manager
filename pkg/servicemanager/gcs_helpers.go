package servicemanager

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var (
	// gcsBucketValidationRegex validates that a name contains only valid characters.
	// It does not enforce all GCS rules (e.g., no IP addresses, no "goog" prefix).
	gcsBucketValidationRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-_.]{1,61}[a-z0-9]$`)
)

const (
	maxBucketNameLength = 63
)

// GenerateTestBucketName creates a unique, valid GCS bucket name for testing purposes.
// It takes a prefix, appends a UUID, and sanitizes it to comply with GCS naming rules.
func GenerateTestBucketName(prefix string) string {
	// Generate a unique UUID.
	id := uuid.New().String()

	// Remove hyphens from UUID for simplicity.
	uniqueID := strings.ReplaceAll(id, "-", "")

	// Construct the unique bucket name using the prefix and the sanitized unique ID.
	bucketName := fmt.Sprintf("%s-%s", prefix, uniqueID)

	// Ensure the bucket name is lowercase, as required by GCS.
	bucketName = strings.ToLower(bucketName)

	// Trim the bucket name to the maximum allowed length of 63 characters.
	if len(bucketName) > maxBucketNameLength {
		bucketName = bucketName[:maxBucketNameLength]
	}

	// After trimming, it's possible the name ends with a hyphen, which is invalid.
	// If so, we trim that last character.
	if strings.HasSuffix(bucketName, "-") || strings.HasSuffix(bucketName, "_") {
		bucketName = bucketName[:maxBucketNameLength-1]
	}

	return bucketName
}

// IsValidBucketName checks if a given string is a plausible GCS bucket name.
// This is useful for pre-flight checks before attempting to create a bucket.
func IsValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	// Note: This simplified check doesn't cover all GCS naming restrictions,
	// such as the rule against names formatted as IP addresses or containing "goog".
	// For testing purposes, this level of validation is generally sufficient.
	return gcsBucketValidationRegex.MatchString(name)
}
