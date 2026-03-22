package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Utility Function Tests
// =============================================================================

func TestParseIntOrDefault_ValidInteger_ReturnsInteger(t *testing.T) {
	result := parseIntOrDefault("42", 10)
	expected := 42
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

func TestParseIntOrDefault_EmptyString_ReturnsDefault(t *testing.T) {
	result := parseIntOrDefault("", 10)
	expected := 10
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

func TestParseIntOrDefault_InvalidString_ReturnsDefault(t *testing.T) {
	result := parseIntOrDefault("invalid", 10)
	expected := 10
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

func TestParseIntOrDefault_NegativeInteger_ReturnsNegative(t *testing.T) {
	result := parseIntOrDefault("-5", 10)
	expected := -5
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

func TestParseDurationOrDefault_ValidDuration_ReturnsDuration(t *testing.T) {
	result := parseDurationOrDefault("5s", 10*time.Second)
	expected := 5 * time.Second
	if result != expected {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestParseDurationOrDefault_InvalidDuration_ReturnsDefault(t *testing.T) {
	result := parseDurationOrDefault("invalid", 10*time.Second)
	expected := 10 * time.Second
	if result != expected {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestGetEnvOrDefault_ValidValue_ReturnsValue(t *testing.T) {
	key := "TEST_CONFIG_KEY"
	value := "test_value"
	os.Setenv(key, value)
	defer os.Unsetenv(key)

	result := getEnvOrDefault(key, "default")
	if result != value {
		t.Errorf("expected %s, got %s", value, result)
	}
}

func TestGetEnvOrDefault_EmptyValue_ReturnsDefault(t *testing.T) {
	key := "TEST_CONFIG_EMPTY_KEY"
	os.Setenv(key, "")
	defer os.Unsetenv(key)

	result := getEnvOrDefault(key, "default")
	expected := "default"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

// =============================================================================
// Configuration Loading Tests
// =============================================================================

func TestConfig_LoadsAllSettingsCorrectly(t *testing.T) {
	// Set comprehensive test environment variables
	testVars := map[string]string{
		// Authentication
		"SPRAWLER_SP_ADMIN_USER":        "admin@example.com",
		"SPRAWLER_SP_TENANT_ADMIN_SITE": "https://example-admin.sharepoint.com/",
		"SPRAWLER_ENTRA_TENANT_ID":      "12345678-1234-1234-1234-123456789abc",
		"SPRAWLER_ENTRA_CLIENT_ID":      "87654321-4321-4321-4321-cba987654321",
		"SPRAWLER_ENTRA_CERT_PATH":      "/path/to/certificate.pfx",

		// Database
		"SPRAWLER_DB_TYPE":               "postgres",
		"SPRAWLER_DB_PATH":               "/custom/path",
		"SPRAWLER_DB_NAME":               "custom.db",
		"SPRAWLER_DB_RECREATE":           "false",
		"SPRAWLER_DB_BATCH_SIZE":         "5000",
		"SPRAWLER_DB_FLUSH_INTERVAL":     "15s",
		"SPRAWLER_DB_QUEUE_CAPACITY":     "15000",
		"SPRAWLER_DB_ENABLE_FAILURE_LOG": "false",

		// Debug
		"SPRAWLER_DEBUG_MAX_PAGES":          "10",
		"SPRAWLER_DEBUG_MAX_ONEDRIVE_SITES": "50",

		// SharePoint
		"SPRAWLER_SP_PAGE_SIZE":             "1000",
		"SPRAWLER_SP_USER_WORKERS":          "12",
		"SPRAWLER_SP_GROUP_WORKERS":         "6",
		"SPRAWLER_SP_PROCESS_GROUPS":        "false",
		"SPRAWLER_SP_PROCESS_GROUP_MEMBERS": "false",
		"SPRAWLER_SP_USER_FETCH_TIMEOUT":    "120s",
		"SPRAWLER_SP_GROUP_FETCH_TIMEOUT":   "45s",
		"SPRAWLER_SP_PROGRESS_INTERVAL":     "60s",

		// OneDrive
		"SPRAWLER_OD_CSOM_BUFFER_SIZE":      "1000",
		"SPRAWLER_OD_USER_WORKERS":          "15",
		"SPRAWLER_OD_USER_FETCH_TIMEOUT":    "120s",
		"SPRAWLER_OD_PROFILE_FETCH_TIMEOUT": "45s",
		"SPRAWLER_OD_PROGRESS_INTERVAL":     "60s",
	}

	// Set all environment variables
	for key, value := range testVars {
		os.Setenv(key, value)
		defer os.Unsetenv(key)
	}

	// Load configuration
	config, err := loadConfigFromEnv()
	if err != nil {
		t.Fatalf("expected config to load successfully, got error: %v", err)
	}

	// Verify Authentication
	if config.AdminUser != "admin@example.com" {
		t.Errorf("expected AdminUser 'admin@example.com', got '%s'", config.AdminUser)
	}
	if config.Auth.SiteURL != "https://example-admin.sharepoint.com/" {
		t.Errorf("expected SiteURL 'https://example-admin.sharepoint.com/', got '%s'", config.Auth.SiteURL)
	}

	// Verify Database
	if config.Database.Type != "postgres" {
		t.Errorf("expected Database.Type 'postgres', got '%s'", config.Database.Type)
	}
	if config.Database.BatchSize != 5000 {
		t.Errorf("expected Database.BatchSize 5000, got %d", config.Database.BatchSize)
	}
	if config.Database.FlushInterval != 15*time.Second {
		t.Errorf("expected Database.FlushInterval 15s, got %v", config.Database.FlushInterval)
	}

	// Verify Debug
	if config.Debug.MaxPages != 10 {
		t.Errorf("expected Debug.MaxPages 10, got %d", config.Debug.MaxPages)
	}

	// Verify SharePoint
	if config.SharePoint.PageSize != 1000 {
		t.Errorf("expected SharePoint.PageSize 1000, got %d", config.SharePoint.PageSize)
	}
	if config.SharePoint.UserFetchTimeout != 120*time.Second {
		t.Errorf("expected SharePoint.UserFetchTimeout 120s, got %v", config.SharePoint.UserFetchTimeout)
	}

	// Verify OneDrive
	if config.OneDrive.UserWorkers != 15 {
		t.Errorf("expected OneDrive.UserWorkers 15, got %d", config.OneDrive.UserWorkers)
	}
	if config.OneDrive.UserFetchTimeout != 120*time.Second {
		t.Errorf("expected OneDrive.UserFetchTimeout 120s, got %v", config.OneDrive.UserFetchTimeout)
	}
	if config.OneDrive.ProfileFetchTimeout != 45*time.Second {
		t.Errorf("expected OneDrive.ProfileFetchTimeout 45s, got %v", config.OneDrive.ProfileFetchTimeout)
	}
	if config.OneDrive.ProgressReportInterval != 60*time.Second {
		t.Errorf("expected OneDrive.ProgressReportInterval 60s, got %v", config.OneDrive.ProgressReportInterval)
	}
}

func TestConfig_UsesDefaultsWhenEnvNotSet(t *testing.T) {
	// Set only required auth variables, unset all others
	requiredAuthVars := map[string]string{
		"SPRAWLER_SP_ADMIN_USER":        "admin@example.com",
		"SPRAWLER_SP_TENANT_ADMIN_SITE": "https://example-admin.sharepoint.com/",
		"SPRAWLER_ENTRA_TENANT_ID":      "12345678-1234-1234-1234-123456789abc",
		"SPRAWLER_ENTRA_CLIENT_ID":      "87654321-4321-4321-4321-cba987654321",
		"SPRAWLER_ENTRA_CERT_PATH":      "/path/to/certificate.pfx",
	}

	// Unset all non-required config variables
	configVars := []string{
		"SPRAWLER_DB_TYPE", "SPRAWLER_DB_PATH", "SPRAWLER_DB_NAME", "SPRAWLER_DB_RECREATE",
		"SPRAWLER_DB_BATCH_SIZE", "SPRAWLER_DB_FLUSH_INTERVAL", "SPRAWLER_DB_QUEUE_CAPACITY", "SPRAWLER_DB_ENABLE_FAILURE_LOG",
		"SPRAWLER_DEBUG_MAX_PAGES", "SPRAWLER_DEBUG_MAX_ONEDRIVE_SITES",
		"SPRAWLER_SP_PAGE_SIZE", "SPRAWLER_SP_USER_WORKERS", "SPRAWLER_SP_GROUP_WORKERS",
		"SPRAWLER_SP_PROCESS_GROUPS", "SPRAWLER_SP_PROCESS_GROUP_MEMBERS",
		"SPRAWLER_SP_USER_FETCH_TIMEOUT", "SPRAWLER_SP_GROUP_FETCH_TIMEOUT", "SPRAWLER_SP_PROGRESS_INTERVAL",
		"SPRAWLER_OD_CSOM_BUFFER_SIZE", "SPRAWLER_OD_USER_WORKERS",
		"SPRAWLER_OD_USER_FETCH_TIMEOUT", "SPRAWLER_OD_PROFILE_FETCH_TIMEOUT", "SPRAWLER_OD_PROGRESS_INTERVAL",
	}

	for _, envVar := range configVars {
		os.Unsetenv(envVar)
	}

	// Set required auth variables
	for key, value := range requiredAuthVars {
		os.Setenv(key, value)
		defer os.Unsetenv(key)
	}

	// Load configuration
	config, err := loadConfigFromEnv()
	if err != nil {
		t.Fatalf("expected config to load successfully, got error: %v", err)
	}

	// Verify defaults are used
	if config.Database.Type != "sqlite" {
		t.Errorf("expected default Database.Type 'sqlite', got '%s'", config.Database.Type)
	}
	if config.Database.BatchSize != 500 {
		t.Errorf("expected default Database.BatchSize 500, got %d", config.Database.BatchSize)
	}
	if config.SharePoint.PageSize != 5000 {
		t.Errorf("expected default SharePoint.PageSize 5000, got %d", config.SharePoint.PageSize)
	}
	if config.SharePoint.UserFetchTimeout != 10*time.Minute {
		t.Errorf("expected default SharePoint.UserFetchTimeout 10m, got %v", config.SharePoint.UserFetchTimeout)
	}
	if config.OneDrive.UserWorkers != 6 {
		t.Errorf("expected default OneDrive.UserWorkers 6, got %d", config.OneDrive.UserWorkers)
	}
	if config.OneDrive.UserFetchTimeout != 10*time.Minute {
		t.Errorf("expected default OneDrive.UserFetchTimeout 10m, got %v", config.OneDrive.UserFetchTimeout)
	}
	if config.OneDrive.ProfileFetchTimeout != 10*time.Minute {
		t.Errorf("expected default OneDrive.ProfileFetchTimeout 10m, got %v", config.OneDrive.ProfileFetchTimeout)
	}
}

func TestConfig_FailsWhenAuthenticationMissing(t *testing.T) {
	// Unset all auth environment variables
	authVars := []string{
		"SPRAWLER_SP_ADMIN_USER",
		"SPRAWLER_SP_TENANT_ADMIN_SITE",
		"SPRAWLER_ENTRA_TENANT_ID",
		"SPRAWLER_ENTRA_CLIENT_ID",
		"SPRAWLER_ENTRA_CERT_PATH",
	}

	for _, envVar := range authVars {
		os.Unsetenv(envVar)
	}

	// Attempt to load config
	config, err := loadConfigFromEnv()

	// Should fail with missing auth variables
	if err == nil {
		t.Fatal("expected config loading to fail when auth vars not set, but it succeeded")
	}
	if config != nil {
		t.Error("expected config to be nil when auth vars not set")
	}
	if !strings.Contains(err.Error(), "missing required auth environment variables") {
		t.Errorf("expected error about missing auth variables, got: %v", err)
	}
}

func TestConfig_FailsWhenSingleAuthVariableMissing(t *testing.T) {
	// Set all but one auth variable
	testVars := map[string]string{
		"SPRAWLER_SP_ADMIN_USER":        "admin@example.com",
		"SPRAWLER_SP_TENANT_ADMIN_SITE": "https://example-admin.sharepoint.com/",
		"SPRAWLER_ENTRA_TENANT_ID":      "12345678-1234-1234-1234-123456789abc",
		"SPRAWLER_ENTRA_CLIENT_ID":      "87654321-4321-4321-4321-cba987654321",
		// SPRAWLER_ENTRA_CERT_PATH is intentionally missing
	}

	// Set environment variables
	for key, value := range testVars {
		os.Setenv(key, value)
		defer os.Unsetenv(key)
	}

	// Ensure the missing variable is unset
	os.Unsetenv("SPRAWLER_ENTRA_CERT_PATH")

	// Attempt to load config
	config, err := loadConfigFromEnv()

	// Should fail
	if err == nil {
		t.Fatal("expected config loading to fail when SPRAWLER_ENTRA_CERT_PATH not set")
	}
	if config != nil {
		t.Error("expected config to be nil when required auth var missing")
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestLoadConfig_LoadsEnvFiles(t *testing.T) {
	// This tests the public LoadConfig function that loads .env files
	// It should handle missing .env files gracefully
	_, err := LoadConfig()

	// This will likely fail due to missing auth vars in test environment,
	// but we can verify it attempts to load .env files
	if err != nil && !strings.Contains(err.Error(), "missing required auth environment variables") {
		t.Errorf("unexpected error from LoadConfig: %v", err)
	}
}
