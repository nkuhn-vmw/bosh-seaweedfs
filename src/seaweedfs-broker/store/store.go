package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ServiceInstance represents a provisioned service instance
type ServiceInstance struct {
	ID               string            `json:"id"`
	ServiceID        string            `json:"service_id"`
	PlanID           string            `json:"plan_id"`
	OrganizationGUID string            `json:"organization_guid"`
	SpaceGUID        string            `json:"space_guid"`
	Parameters       map[string]any    `json:"parameters,omitempty"`
	Context          map[string]any    `json:"context,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`

	// For shared plan instances
	BucketName string `json:"bucket_name,omitempty"`

	// For dedicated plan instances
	DeploymentName string `json:"deployment_name,omitempty"`
	S3Endpoint     string `json:"s3_endpoint,omitempty"`
	IAMEndpoint    string `json:"iam_endpoint,omitempty"` // Internal IP:port for IAM operations
	FilerEndpoint  string `json:"filer_endpoint,omitempty"`
	ConsoleURL     string `json:"console_url,omitempty"`
	FilerURL       string `json:"filer_url,omitempty"`
	VolumeURL      string `json:"volume_url,omitempty"`
	AdminAccessKey string `json:"admin_access_key,omitempty"`
	AdminSecretKey string `json:"admin_secret_key,omitempty"`

	// Provisioning state
	State        string `json:"state"` // provisioning, succeeded, failed
	StateMessage string `json:"state_message,omitempty"`
}

// ServiceBinding represents a service binding
type ServiceBinding struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instance_id"`
	AppGUID    string         `json:"app_guid,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`

	// Credentials
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`

	// IAM identity info for cleanup
	IAMUserName string `json:"iam_user_name,omitempty"`
}

// State represents the complete broker state
type State struct {
	Instances map[string]*ServiceInstance `json:"instances"`
	Bindings  map[string]*ServiceBinding  `json:"bindings"`
}

// Store is the interface for persisting broker state
type Store interface {
	GetInstance(instanceID string) (*ServiceInstance, error)
	SaveInstance(instance *ServiceInstance) error
	DeleteInstance(instanceID string) error
	ListInstances() ([]*ServiceInstance, error)

	GetBinding(bindingID string) (*ServiceBinding, error)
	SaveBinding(binding *ServiceBinding) error
	DeleteBinding(bindingID string) error
	ListBindingsForInstance(instanceID string) ([]*ServiceBinding, error)
}

// FileStore implements Store using a JSON file
type FileStore struct {
	path  string
	mu    sync.RWMutex
	state *State
}

// NewFileStore creates a new file-based store
func NewFileStore(path string) (*FileStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	store := &FileStore{
		path: path,
		state: &State{
			Instances: make(map[string]*ServiceInstance),
			Bindings:  make(map[string]*ServiceBinding),
		},
	}

	// Load existing state if file exists
	if _, err := os.Stat(path); err == nil {
		if err := store.load(); err != nil {
			return nil, fmt.Errorf("failed to load state: %w", err)
		}
	}

	return store, nil
}

func (s *FileStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, s.state)
}

func (s *FileStore) save() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

// GetInstance retrieves a service instance by ID
func (s *FileStore) GetInstance(instanceID string) (*ServiceInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	instance, ok := s.state.Instances[instanceID]
	if !ok {
		return nil, nil
	}
	return instance, nil
}

// SaveInstance saves a service instance
func (s *FileStore) SaveInstance(instance *ServiceInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	instance.UpdatedAt = time.Now()
	s.state.Instances[instance.ID] = instance
	return s.save()
}

// DeleteInstance deletes a service instance
func (s *FileStore) DeleteInstance(instanceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.state.Instances, instanceID)
	return s.save()
}

// ListInstances returns all service instances
func (s *FileStore) ListInstances() ([]*ServiceInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	instances := make([]*ServiceInstance, 0, len(s.state.Instances))
	for _, instance := range s.state.Instances {
		instances = append(instances, instance)
	}
	return instances, nil
}

// GetBinding retrieves a service binding by ID
func (s *FileStore) GetBinding(bindingID string) (*ServiceBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	binding, ok := s.state.Bindings[bindingID]
	if !ok {
		return nil, nil
	}
	return binding, nil
}

// SaveBinding saves a service binding
func (s *FileStore) SaveBinding(binding *ServiceBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Bindings[binding.ID] = binding
	return s.save()
}

// DeleteBinding deletes a service binding
func (s *FileStore) DeleteBinding(bindingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.state.Bindings, bindingID)
	return s.save()
}

// ListBindingsForInstance returns all bindings for a service instance
func (s *FileStore) ListBindingsForInstance(instanceID string) ([]*ServiceBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bindings := make([]*ServiceBinding, 0)
	for _, binding := range s.state.Bindings {
		if binding.InstanceID == instanceID {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}
