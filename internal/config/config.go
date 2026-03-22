// Package config loads application configuration from environment files.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"sprawler/internal/logger"

	"github.com/joho/godotenv"
)

// Config holds application configuration.
type Config struct {
	Auth      AuthConfig
	AdminUser string

	Transport TransportConfig
	Database  DatabaseConfig

	Debug DebugConfig

	SharePoint SharePointConfig
	OneDrive   OneDriveConfig
}

// AuthConfig contains credentials for Entra ID certificate-based authentication.
type AuthConfig struct {
	SiteURL  string // Tenant admin site URL
	TenantID string // Entra ID tenant
	ClientID string // App registration client ID
	CertPath string // Path to .pfx/.p12 certificate file
}

// TransportConfig contains HTTP transport and retry settings.
type TransportConfig struct {
	MaxRetries    int             // Max transport retries for transient network errors
	RetryBackoffs []time.Duration // Backoff durations per retry attempt
	ThrottlePause time.Duration   // Default pause when 429/503 lacks Retry-After

	// Gosip HTTP status retry policy
	RetryPolicy map[int]int // status code -> max retries
}

// DatabaseConfig contains database settings.
type DatabaseConfig struct {
	Type     string // "sqlite", "postgres", etc.
	Path     string
	Name     string
	MaxConns int
	Recreate bool

	// BatchWriter performance settings
	BatchSize        int           // Items per batch write
	FlushInterval    time.Duration // Time-based flush interval
	QueueCapacity    int           // Queue capacity for backpressure
	EnableFailureLog bool          // Enable failure tracking
	FailureLogPath   string        // Path for failed writes NDJSON log

	// SQLite tuning
	BusyTimeout int // Lock wait timeout in milliseconds
	CacheSize   int // Page cache size (negative = KB)
}

// DebugConfig contains debug settings.
type DebugConfig struct {
	MaxPages         int // 0 = no limit, >0 = limit for debugging
	MaxOneDriveSites int // OneDrive-specific debug limit
}

// SharePointConfig contains SharePoint processing settings.
type SharePointConfig struct {
	PageSize           int      // Results per page (max 5000)
	SiteEnumBufferSize int      // Sites buffered between enumerator and workers
	ExpectedSites      int      // Expected total sites for ETA calculations
	SkipTemplates      []string // Site templates to skip (e.g., TEAMCHANNEL#0, APPCATALOG#0)

	UserWorkers         int  // Workers for site users
	GroupWorkers        int  // Workers for site groups
	ProcessGroups       bool // Enable/disable group processing
	ProcessGroupMembers bool // Enable/disable group member processing

	UserFetchTimeout       time.Duration // Timeout for site user enumeration
	GroupFetchTimeout      time.Duration // Timeout for site group enumeration
	MemberFetchTimeout     time.Duration // Per-group timeout for member fetches
	ProgressReportInterval time.Duration // Interval for progress reporting

	ThrottleRecoveryCooldown time.Duration // Wait period before scaling concurrency back up after 429s
	ThrottleCheckInterval    time.Duration // How often the scaler checks for backpressure signals
}

// OneDriveConfig contains OneDrive processing settings.
type OneDriveConfig struct {
	CSOMBufferSize int // Sites buffered between CSOM and workers
	ExpectedSites  int // Expected total sites for ETA calculations

	UserWorkers int // Concurrent user processing workers

	UserFetchTimeout       time.Duration // Timeout for site user enumeration
	ProfileFetchTimeout    time.Duration // Timeout for user profile retrieval
	ProgressReportInterval time.Duration // Interval for progress reporting

	ThrottleRecoveryCooldown time.Duration // Wait period before scaling concurrency back up after 429s
	ThrottleCheckInterval    time.Duration // How often the scaler checks for backpressure signals
}

// LoadConfig loads configuration from environment files.
func LoadConfig() (*Config, error) {
	// Load .env files if available
	godotenv.Load(".env.local", ".env")

	return loadConfigFromEnv()
}

// loadConfigFromEnv loads configuration from environment variables.
func loadConfigFromEnv() (*Config, error) {
	siteURL := getEnvOrDefault("SPRAWLER_SP_TENANT_ADMIN_SITE", "")
	tenantID := getEnvOrDefault("SPRAWLER_ENTRA_TENANT_ID", "")
	clientID := getEnvOrDefault("SPRAWLER_ENTRA_CLIENT_ID", "")
	certPath := getEnvOrDefault("SPRAWLER_ENTRA_CERT_PATH", "")

	// Check if all required auth variables are present
	if siteURL == "" || tenantID == "" || clientID == "" || certPath == "" {
		return nil, fmt.Errorf("missing required auth environment variables: SPRAWLER_SP_TENANT_ADMIN_SITE, SPRAWLER_ENTRA_TENANT_ID, SPRAWLER_ENTRA_CLIENT_ID, SPRAWLER_ENTRA_CERT_PATH")
	}

	config := &Config{
		AdminUser: getEnvOrDefault("SPRAWLER_SP_ADMIN_USER", ""),
		Auth: AuthConfig{
			SiteURL:  siteURL,
			TenantID: tenantID,
			ClientID: clientID,
			CertPath: certPath,
		},

		Transport: TransportConfig{
			MaxRetries:    parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_MAX_RETRIES", "3"), 3),
			RetryBackoffs: parseDurationSlice(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_BACKOFFS", "5s,10s,30s"), []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}),
			ThrottlePause: parseDurationOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_THROTTLE_PAUSE", "1m"), 1*time.Minute),
			RetryPolicy: map[int]int{
				500: parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_500", "5"), 5),
				503: parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_503", "10"), 10),
				504: parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_504", "10"), 10),
				429: parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_429", "10"), 10),
				401: parseIntOrDefault(getEnvOrDefault("SPRAWLER_TRANSPORT_RETRY_401", "5"), 5),
			},
		},

		Database: DatabaseConfig{
			Type:     getEnvOrDefault("SPRAWLER_DB_TYPE", "sqlite"),
			Path:     getEnvOrDefault("SPRAWLER_DB_PATH", "./data"),
			Name:     getEnvOrDefault("SPRAWLER_DB_NAME", "spo.db"),
			MaxConns: 1,
			Recreate: getEnvOrDefault("SPRAWLER_DB_RECREATE", "true") == "true",

			// BatchWriter performance settings
			BatchSize:        parseIntOrDefault(getEnvOrDefault("SPRAWLER_DB_BATCH_SIZE", "500"), 500),
			FlushInterval:    parseDurationOrDefault(getEnvOrDefault("SPRAWLER_DB_FLUSH_INTERVAL", "5s"), 5*time.Second),
			QueueCapacity:    parseIntOrDefault(getEnvOrDefault("SPRAWLER_DB_QUEUE_CAPACITY", "8000"), 8000),
			EnableFailureLog: getEnvOrDefault("SPRAWLER_DB_ENABLE_FAILURE_LOG", "true") == "true",
			FailureLogPath:   getEnvOrDefault("SPRAWLER_DB_FAILURE_LOG_PATH", "./failed_writes.ndjson"),

			// SQLite tuning
			BusyTimeout: parseIntOrDefault(getEnvOrDefault("SPRAWLER_DB_BUSY_TIMEOUT", "5000"), 5000),
			CacheSize:   parseIntOrDefault(getEnvOrDefault("SPRAWLER_DB_CACHE_SIZE", "-262144"), -262144),
		},

		Debug: DebugConfig{
			MaxPages:         parseIntOrDefault(getEnvOrDefault("SPRAWLER_DEBUG_MAX_PAGES", "0"), 0),
			MaxOneDriveSites: parseIntOrDefault(getEnvOrDefault("SPRAWLER_DEBUG_MAX_ONEDRIVE_SITES", "0"), 0),
		},

		SharePoint: SharePointConfig{
			PageSize:            parseIntOrDefault(getEnvOrDefault("SPRAWLER_SP_PAGE_SIZE", "5000"), 5000),
			SiteEnumBufferSize:  parseIntOrDefault(getEnvOrDefault("SPRAWLER_SP_SITE_ENUM_BUFFER_SIZE", "400"), 400),
			ExpectedSites:       parseIntOrDefault(getEnvOrDefault("SPRAWLER_SP_EXPECTED_SITES", "0"), 0),
			SkipTemplates:       parseStringSlice(getEnvOrDefault("SPRAWLER_SP_SKIP_TEMPLATES", "TEAMCHANNEL#0,TEAMCHANNEL#1,APPCATALOG#0,REDIRECTSITE#0")),
			UserWorkers:         parseIntOrDefault(getEnvOrDefault("SPRAWLER_SP_USER_WORKERS", "6"), 6),
			GroupWorkers:        parseIntOrDefault(getEnvOrDefault("SPRAWLER_SP_GROUP_WORKERS", "4"), 4),
			ProcessGroups:       getEnvOrDefault("SPRAWLER_SP_PROCESS_GROUPS", "false") == "true",
			ProcessGroupMembers: getEnvOrDefault("SPRAWLER_SP_PROCESS_GROUP_MEMBERS", "false") == "true",

			UserFetchTimeout:       parseDurationOrDefault(getEnvOrDefault("SPRAWLER_SP_USER_FETCH_TIMEOUT", "10m"), 10*time.Minute),
			GroupFetchTimeout:      parseDurationOrDefault(getEnvOrDefault("SPRAWLER_SP_GROUP_FETCH_TIMEOUT", "10m"), 10*time.Minute),
			MemberFetchTimeout:     parseDurationOrDefault(getEnvOrDefault("SPRAWLER_SP_MEMBER_FETCH_TIMEOUT", "10m"), 10*time.Minute),
			ProgressReportInterval: parseDurationOrDefault(getEnvOrDefault("SPRAWLER_SP_PROGRESS_INTERVAL", "1m"), 1*time.Minute),

			ThrottleRecoveryCooldown: parseDurationOrDefault(getEnvOrDefault("SPRAWLER_THROTTLE_RECOVERY_COOLDOWN", "1m"), 1*time.Minute),
			ThrottleCheckInterval:    parseDurationOrDefault(getEnvOrDefault("SPRAWLER_THROTTLE_CHECK_INTERVAL", "10s"), 10*time.Second),
		},

		OneDrive: OneDriveConfig{
			CSOMBufferSize: parseIntOrDefault(getEnvOrDefault("SPRAWLER_OD_CSOM_BUFFER_SIZE", "500"), 500),
			ExpectedSites:  parseIntOrDefault(getEnvOrDefault("SPRAWLER_OD_EXPECTED_SITES", "0"), 0),
			UserWorkers:    parseIntOrDefault(getEnvOrDefault("SPRAWLER_OD_USER_WORKERS", "6"), 6),

			UserFetchTimeout:       parseDurationOrDefault(getEnvOrDefault("SPRAWLER_OD_USER_FETCH_TIMEOUT", "10m"), 10*time.Minute),
			ProfileFetchTimeout:    parseDurationOrDefault(getEnvOrDefault("SPRAWLER_OD_PROFILE_FETCH_TIMEOUT", "10m"), 10*time.Minute),
			ProgressReportInterval: parseDurationOrDefault(getEnvOrDefault("SPRAWLER_OD_PROGRESS_INTERVAL", "1m"), 1*time.Minute),

			ThrottleRecoveryCooldown: parseDurationOrDefault(getEnvOrDefault("SPRAWLER_THROTTLE_RECOVERY_COOLDOWN", "1m"), 1*time.Minute),
			ThrottleCheckInterval:    parseDurationOrDefault(getEnvOrDefault("SPRAWLER_THROTTLE_CHECK_INTERVAL", "10s"), 10*time.Second),
		},
	}

	config.LogConfig()
	return config, nil
}

// getEnvOrDefault gets environment variable or returns default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Parsing utilities
func parseIntOrDefault(value string, defaultValue int) int {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed
	}
	return defaultValue
}

func parseDurationOrDefault(value string, defaultValue time.Duration) time.Duration {
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}
	return defaultValue
}

func parseStringSlice(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			result = append(result, s)
		}
	}
	return result
}

func parseDurationSlice(value string, defaults []time.Duration) []time.Duration {
	parts := strings.Split(value, ",")
	result := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		d, err := time.ParseDuration(strings.TrimSpace(p))
		if err != nil {
			return defaults
		}
		result = append(result, d)
	}
	if len(result) == 0 {
		return defaults
	}
	return result
}

// LogConfig logs the configuration in a readable format.
func (c *Config) LogConfig() {
	l := logger.NewLogger("Main")

	l.Infof("Config: DB=%s/%s (recreate=%t), batch=%d, flush=%s, queue=%d",
		c.Database.Path, c.Database.Name, c.Database.Recreate,
		c.Database.BatchSize, c.Database.FlushInterval, c.Database.QueueCapacity)

	spExpected := "not set"
	if c.SharePoint.ExpectedSites > 0 {
		spExpected = fmt.Sprintf("%d", c.SharePoint.ExpectedSites)
	}
	odExpected := "not set"
	if c.OneDrive.ExpectedSites > 0 {
		odExpected = fmt.Sprintf("%d", c.OneDrive.ExpectedSites)
	}
	l.Infof("Config: SP workers=%d+%d, pageSize=%d, expected=%s | OD workers=%d, buf=%d, expected=%s",
		c.SharePoint.UserWorkers, c.SharePoint.GroupWorkers, c.SharePoint.PageSize, spExpected,
		c.OneDrive.UserWorkers, c.OneDrive.CSOMBufferSize, odExpected)

	if c.Debug.MaxPages > 0 || c.Debug.MaxOneDriveSites > 0 {
		l.Infof("Config: debug maxPages=%d, maxODSites=%d",
			c.Debug.MaxPages, c.Debug.MaxOneDriveSites)
	}

	if c.Auth.SiteURL != "" {
		l.Infof("Config: admin=%s (%s)", c.AdminUser, c.Auth.SiteURL)
	}
}
