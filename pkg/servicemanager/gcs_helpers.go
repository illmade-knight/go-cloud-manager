package servicemanager

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var (
	// gcsBucketValidationRegex validates that a name contains only valid characters.
	// Note: This does not enforce all GCS rules (e.g., cannot be an IP address,
	// cannot start with "goog", cannot contain "google"). It serves as a basic sanity check.
	gcsBucketValidationRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-_.]{1,61}[a-z0-9]$`)
)

const (
	// maxBucketNameLength is the maximum character length for a GCS bucket name.
	maxBucketNameLength = 63
)

// GenerateTestBucketName creates a unique, valid GCS bucket name for testing purposes.
// It takes a prefix, appends a UUID, and sanitizes the result to comply with GCS naming rules,
// such as being lowercase and within the required length.
func GenerateTestBucketName(prefix string) string {
	// Generate a unique identifier and remove hyphens for compactness.
	uniqueID := strings.ReplaceAll(uuid.New().String(), "-", "")

	// Construct the full bucket name and convert to lowercase as required by GCS.
	bucketName := strings.ToLower(fmt.Sprintf("%s-%s", prefix, uniqueID))

	// Trim the bucket name to the maximum allowed length if necessary.
	if len(bucketName) > maxBucketNameLength {
		bucketName = bucketName[:maxBucketNameLength]
	}

	// After trimming, the name might end with a hyphen or underscore, which is invalid.
	// Trim the last character if this is the case.
	if strings.HasSuffix(bucketName, "-") || strings.HasSuffix(bucketName, "_") {
		bucketName = bucketName[:len(bucketName)-1]
	}

	return bucketName
}

// IsValidBucketName checks if a given string is a plausible GCS bucket name.
// This is useful for pre-flight validation before attempting to create a bucket.
// It checks for length and valid characters but does not cover all GCS naming restrictions
// (e.g., the rule against names formatted as IP addresses).
func IsValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > maxBucketNameLength {
		return false
	}
	return gcsBucketValidationRegex.MatchString(name)
}
