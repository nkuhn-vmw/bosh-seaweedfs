package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// ConfigVersion is used to track config schema changes and force recompilation
const ConfigVersion = "2.0.0-nested-structs"

// Config represents the broker configuration
type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	LogLevel   string `yaml:"log_level"`

	// Basic auth credentials for the broker API
	Auth AuthConfig `yaml:"auth"`

	// TLS configuration
	TLS TLSConfig `yaml:"tls"`

	// Catalog configuration
	Catalog CatalogConfig `yaml:"catalog"`

	// Shared SeaweedFS cluster configuration
	SharedCluster SharedClusterConfig `yaml:"shared_cluster"`

	// BOSH configuration for on-demand instances
	BOSH BOSHConfig `yaml:"bosh"`

	// Cloud Foundry configuration
	CF CFConfig `yaml:"cf"`

	// NATS configuration for on-demand route registration
	NATS NATSConfig `yaml:"nats"`

	// State store configuration
	StateStore StateStoreConfig `yaml:"state_store"`

	// CredHub configuration for credential storage
	CredHub CredHubConfig `yaml:"credhub"`

	// Syslog configuration for on-demand deployments
	Syslog SyslogConfig `yaml:"syslog"`

	// OTEL configuration for on-demand deployments
	OTEL OTELConfig `yaml:"otel"`

	// Backup configuration for on-demand deployments
	Backup BackupConfig `yaml:"backup"`
}

// CFConfig holds Cloud Foundry configuration
type CFConfig struct {
	SystemDomain   string `yaml:"system_domain"`
	AppsDomain     string `yaml:"apps_domain"`
	DeploymentName string `yaml:"deployment_name"`
}

// AuthConfig holds authentication credentials
type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// TLSConfig holds TLS settings
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// CatalogConfig holds service catalog configuration
type CatalogConfig struct {
	Services []ServiceConfig `yaml:"services"`
}

// ServiceConfig represents a service in the catalog
type ServiceConfig struct {
	ID          string          `yaml:"id"`
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Bindable    bool            `yaml:"bindable"`
	Tags        []string        `yaml:"tags"`
	Metadata    ServiceMetadata `yaml:"metadata"`
	Plans       []PlanConfig    `yaml:"plans"`
}

// ServiceMetadata holds service-level metadata
type ServiceMetadata struct {
	DisplayName         string `yaml:"displayName"`
	ImageURL            string `yaml:"imageUrl"`
	LongDescription     string `yaml:"longDescription"`
	ProviderDisplayName string `yaml:"providerDisplayName"`
	DocumentationURL    string `yaml:"documentationUrl"`
	SupportURL          string `yaml:"supportUrl"`
}

// PlanConfig represents a service plan
type PlanConfig struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Free        bool         `yaml:"free"`
	Metadata    PlanMetadata `yaml:"metadata"`
	// PlanType: "shared" or "dedicated"
	PlanType string `yaml:"plan_type"`
	// DeploymentType: "single_node" or "ha" (for dedicated plans)
	DeploymentType string `yaml:"deployment_type"`
	// StorageQuotaGB is the max storage quota for this plan
	StorageQuotaGB int `yaml:"storage_quota_gb"`
	// DedicatedConfig is used for dedicated plans
	DedicatedConfig *DedicatedPlanConfig `yaml:"dedicated_config,omitempty"`
}

// PlanMetadata holds plan metadata including bullets
type PlanMetadata struct {
	DisplayName string   `yaml:"displayName"`
	Bullets     []string `yaml:"bullets"`
}

// DedicatedPlanConfig holds configuration for dedicated cluster plans
type DedicatedPlanConfig struct {
	VMType            string   `yaml:"vm_type"`
	DiskType          string   `yaml:"disk_type"`
	MasterNodes       int      `yaml:"master_nodes"`
	VolumeNodes       int      `yaml:"volume_nodes"`
	FilerNodes        int      `yaml:"filer_nodes"`
	Replication       string   `yaml:"replication"`
	Network           string   `yaml:"network"`
	AZs               []string `yaml:"azs"`
	EnableMasterRoute bool     `yaml:"enable_master_route"`
	EnableFilerRoute  bool     `yaml:"enable_filer_route"`
	EnableVolumeRoute bool     `yaml:"enable_volume_route"`
	EnableAdminRoute  bool     `yaml:"enable_admin_route"`
}

// SharedClusterConfig holds configuration for the shared SeaweedFS cluster
type SharedClusterConfig struct {
	Enabled       bool   `yaml:"enabled"`
	S3Endpoint    string `yaml:"s3_endpoint"`
	IAMEndpoint   string `yaml:"iam_endpoint"` // Internal endpoint for IAM operations (no gorouter)
	FilerEndpoint string `yaml:"filer_endpoint"`
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	UseSSL        bool   `yaml:"use_ssl"`
	UseDNS        bool   `yaml:"use_dns"`
	Region        string `yaml:"region"`
}

// BOSHConfig holds BOSH director configuration for on-demand deployments
type BOSHConfig struct {
	URL            string             `yaml:"url"`
	RootCACert     string             `yaml:"root_ca_cert"`
	Authentication BOSHAuthentication `yaml:"authentication"`

	// Deployment template configuration
	DeploymentPrefix      string `yaml:"deployment_prefix"`
	ReleaseName           string `yaml:"release_name"`
	ReleaseVersion        string `yaml:"release_version"`
	StemcellOS            string `yaml:"stemcell_os"`
	StemcellVersion       string `yaml:"stemcell_version"`
	RoutingReleaseVersion string `yaml:"routing_release_version"`
}

// NATSConfig holds NATS configuration for on-demand route registration
type NATSConfig struct {
	Machines []string      `yaml:"machines"`
	Port     int           `yaml:"port"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	TLS      NATSTLSConfig `yaml:"tls"`
}

// NATSTLSConfig holds NATS TLS settings
type NATSTLSConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ClientCert string `yaml:"client_cert"`
	ClientKey  string `yaml:"client_key"`
	CACert     string `yaml:"ca_cert"`
}

// BOSHAuthentication holds BOSH authentication configuration
type BOSHAuthentication struct {
	UAA BOSHUAAConfig `yaml:"uaa"`
}

// BOSHUAAConfig holds UAA credentials for BOSH access
type BOSHUAAConfig struct {
	URL          string `yaml:"url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// StateStoreConfig holds configuration for the state store
type StateStoreConfig struct {
	// Type: "file" or "database"
	Type string `yaml:"type"`
	// Path is used for file-based state store
	Path string `yaml:"path"`
	// DatabaseURL is used for database-based state store
	DatabaseURL string `yaml:"database_url"`
}

// CredHubConfig holds CredHub configuration for credential storage
type CredHubConfig struct {
	URL          string `yaml:"url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	CACert       string `yaml:"ca_cert"`
}

// SyslogConfig holds syslog forwarding configuration for on-demand deployments
type SyslogConfig struct {
	Address       string `yaml:"address"`
	Transport     string `yaml:"transport"`
	TLSEnabled    bool   `yaml:"tls_enabled"`
	CACert        string `yaml:"ca_cert"`
	PermittedPeer string `yaml:"permitted_peer"`
}

// OTELConfig holds OpenTelemetry configuration for on-demand deployments
type OTELConfig struct {
	OTLPEndpoint      string `yaml:"otlp_endpoint"`
	OTLPProtocol      string `yaml:"otlp_protocol"`
	OTLPCACert        string `yaml:"otlp_ca_cert"`
	ScrapeInterval    string `yaml:"scrape_interval"`
	EnableHostMetrics bool   `yaml:"enable_host_metrics"`
}

// BackupConfig holds backup configuration for on-demand deployments
type BackupConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Schedule        string `yaml:"schedule"`
	DestinationType string `yaml:"destination_type"`
	LocalPath       string `yaml:"local_path"`
	S3Endpoint      string `yaml:"s3_endpoint"`
	S3Bucket        string `yaml:"s3_bucket"`
	S3AccessKey     string `yaml:"s3_access_key"`
	S3SecretKey     string `yaml:"s3_secret_key"`
	RetentionCount  int    `yaml:"retention_count"`
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Set defaults
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.StateStore.Type == "" {
		cfg.StateStore.Type = "file"
	}
	if cfg.StateStore.Path == "" {
		cfg.StateStore.Path = "/var/vcap/store/seaweedfs-broker/state.json"
	}
	if cfg.SharedCluster.Region == "" {
		cfg.SharedCluster.Region = "us-east-1"
	}
	if cfg.Syslog.Transport == "" {
		cfg.Syslog.Transport = "tcp"
	}
	if cfg.OTEL.OTLPProtocol == "" {
		cfg.OTEL.OTLPProtocol = "grpc"
	}
	if cfg.OTEL.ScrapeInterval == "" {
		cfg.OTEL.ScrapeInterval = "30s"
	}
	if cfg.Backup.Schedule == "" {
		cfg.Backup.Schedule = "0 2 * * *"
	}
	if cfg.Backup.DestinationType == "" {
		cfg.Backup.DestinationType = "local"
	}
	if cfg.Backup.LocalPath == "" {
		cfg.Backup.LocalPath = "/var/vcap/store/backups"
	}
	if cfg.Backup.RetentionCount == 0 {
		cfg.Backup.RetentionCount = 7
	}

	return cfg, nil
}
