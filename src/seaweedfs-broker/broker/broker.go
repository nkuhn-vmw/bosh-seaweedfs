package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/cloudfoundry/seaweedfs-broker/bosh"
	"github.com/cloudfoundry/seaweedfs-broker/config"
	"github.com/cloudfoundry/seaweedfs-broker/iam"
	"github.com/cloudfoundry/seaweedfs-broker/store"
)

const (
	// OSB API version
	OSBAPIVersion = "2.17"

	// Plan types
	PlanTypeShared    = "shared"
	PlanTypeDedicated = "dedicated"
)

// Broker implements the Open Service Broker API
type Broker struct {
	config     *config.Config
	store      store.Store
	boshClient *bosh.Client
	s3Client   *minio.Client
	iamClient  *iam.Client
}

// New creates a new broker instance
func New(cfg *config.Config) (*Broker, error) {
	// Initialize state store
	stateStore, err := store.NewFileStore(cfg.StateStore.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	b := &Broker{
		config: cfg,
		store:  stateStore,
	}

	// Initialize BOSH client if configured
	if cfg.BOSH.URL != "" {
		boshClient, err := bosh.NewClient(&cfg.BOSH)
		if err != nil {
			return nil, fmt.Errorf("failed to create BOSH client: %w", err)
		}
		b.boshClient = boshClient
	}

	// Initialize S3 client for shared cluster
	if cfg.SharedCluster.S3Endpoint != "" {
		s3Client, err := minio.New(cfg.SharedCluster.S3Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.SharedCluster.AccessKey, cfg.SharedCluster.SecretKey, ""),
			Secure: cfg.SharedCluster.UseSSL,
			Region: cfg.SharedCluster.Region,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create S3 client: %w", err)
		}
		b.s3Client = s3Client

		// Initialize IAM client for dynamic credential management
		// Use the internal IAM endpoint (direct BOSH connection) to bypass gorouter
		// which may interfere with IAM API requests
		iamEndpoint := cfg.SharedCluster.IAMEndpoint
		if iamEndpoint == "" {
			// Fall back to S3 endpoint if IAM endpoint not configured
			iamEndpoint = cfg.SharedCluster.S3Endpoint
		}
		// Internal BOSH endpoints don't use SSL
		iamUseSSL := false
		if iamEndpoint == cfg.SharedCluster.S3Endpoint {
			iamUseSSL = cfg.SharedCluster.UseSSL
		}
		log.Printf("Initializing IAM client with endpoint: %s (SSL: %v)", iamEndpoint, iamUseSSL)
		b.iamClient = iam.NewClient(
			iamEndpoint,
			cfg.SharedCluster.AccessKey,
			cfg.SharedCluster.SecretKey,
			cfg.SharedCluster.Region,
			iamUseSSL,
		)
	}

	return b, nil
}

// Router returns the HTTP router for the broker
func (b *Broker) Router() http.Handler {
	r := mux.NewRouter()

	// Health check endpoint (no auth required)
	r.HandleFunc("/health", b.healthHandler).Methods("GET")

	// OSB API endpoints
	api := r.PathPrefix("/v2").Subrouter()
	api.Use(b.authMiddleware)
	api.Use(b.osbVersionMiddleware)

	api.HandleFunc("/catalog", b.catalogHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}", b.provisionHandler).Methods("PUT")
	api.HandleFunc("/service_instances/{instance_id}", b.deprovisionHandler).Methods("DELETE")
	api.HandleFunc("/service_instances/{instance_id}", b.getInstanceHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}/last_operation", b.lastOperationHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.bindHandler).Methods("PUT")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.unbindHandler).Methods("DELETE")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.getBindingHandler).Methods("GET")

	return r
}

// Middleware

func (b *Broker) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != b.config.Auth.Username || password != b.config.Auth.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="SeaweedFS Service Broker"`)
			b.writeError(w, http.StatusUnauthorized, "Unauthorized", "Invalid or missing credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *Broker) osbVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("X-Broker-API-Version")
		if version == "" {
			b.writeError(w, http.StatusPreconditionFailed, "MissingAPIVersion",
				"X-Broker-API-Version header is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler implementations

func (b *Broker) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (b *Broker) catalogHandler(w http.ResponseWriter, r *http.Request) {
	catalog := b.buildCatalog()
	b.writeJSON(w, http.StatusOK, catalog)
}

func (b *Broker) provisionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	// Check if instance already exists
	existing, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if existing != nil {
		// Return 200 if same parameters, 409 if different
		b.writeJSON(w, http.StatusOK, map[string]any{
			"dashboard_url": b.getDashboardURL(existing),
		})
		return
	}

	// Parse request
	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		b.writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON body")
		return
	}

	// Validate service and plan
	plan := b.findPlan(req.ServiceID, req.PlanID)
	if plan == nil {
		b.writeError(w, http.StatusBadRequest, "InvalidPlan", "Unknown service or plan ID")
		return
	}

	// Check async requirement for dedicated plans
	acceptsAsync := r.URL.Query().Get("accepts_incomplete") == "true"
	if plan.PlanType == PlanTypeDedicated && !acceptsAsync {
		b.writeError(w, http.StatusUnprocessableEntity, "AsyncRequired",
			"This plan requires asynchronous provisioning")
		return
	}

	// Create instance
	instance := &store.ServiceInstance{
		ID:               instanceID,
		ServiceID:        req.ServiceID,
		PlanID:           req.PlanID,
		OrganizationGUID: req.OrganizationGUID,
		SpaceGUID:        req.SpaceGUID,
		Parameters:       req.Parameters,
		Context:          req.Context,
		CreatedAt:        time.Now(),
		State:            "provisioning",
	}

	if plan.PlanType == PlanTypeShared {
		// Provision shared bucket synchronously
		if err := b.provisionSharedBucket(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "ProvisionError", err.Error())
			return
		}
		instance.State = "succeeded"
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		b.writeJSON(w, http.StatusCreated, map[string]any{
			"dashboard_url": b.getDashboardURL(instance),
		})
	} else {
		// Provision dedicated cluster asynchronously
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		go b.provisionDedicatedCluster(instance, plan)
		b.writeJSON(w, http.StatusAccepted, map[string]any{
			"dashboard_url": b.getDashboardURL(instance),
			"operation":     "provision",
		})
	}
}

func (b *Broker) deprovisionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	plan := b.findPlan(instance.ServiceID, instance.PlanID)
	acceptsAsync := r.URL.Query().Get("accepts_incomplete") == "true"

	// Check for existing bindings
	bindings, err := b.store.ListBindingsForInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if len(bindings) > 0 {
		b.writeError(w, http.StatusBadRequest, "BindingsExist",
			"Cannot deprovision instance with active bindings")
		return
	}

	if plan != nil && plan.PlanType == PlanTypeDedicated {
		if !acceptsAsync {
			b.writeError(w, http.StatusUnprocessableEntity, "AsyncRequired",
				"This plan requires asynchronous deprovisioning")
			return
		}
		instance.State = "deprovisioning"
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		go b.deprovisionDedicatedCluster(instance)
		b.writeJSON(w, http.StatusAccepted, map[string]any{
			"operation": "deprovision",
		})
	} else {
		// Deprovision shared bucket synchronously
		if err := b.deprovisionSharedBucket(instance); err != nil {
			log.Printf("Warning: failed to delete bucket: %v", err)
		}
		if err := b.store.DeleteInstance(instanceID); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		b.writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func (b *Broker) getInstanceHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	b.writeJSON(w, http.StatusOK, map[string]any{
		"service_id":    instance.ServiceID,
		"plan_id":       instance.PlanID,
		"dashboard_url": b.getDashboardURL(instance),
		"parameters":    instance.Parameters,
	})
}

func (b *Broker) lastOperationHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	state := "in progress"
	switch instance.State {
	case "succeeded":
		state = "succeeded"
	case "failed":
		state = "failed"
	}

	response := map[string]any{
		"state": state,
	}
	if instance.StateMessage != "" {
		response["description"] = instance.StateMessage
	}

	b.writeJSON(w, http.StatusOK, response)
}

func (b *Broker) bindHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	// Check if instance is ready
	if instance.State != "succeeded" {
		b.writeError(w, http.StatusUnprocessableEntity, "InstanceNotReady",
			"Service instance is not ready")
		return
	}

	// Check if binding already exists
	existing, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if existing != nil {
		b.writeJSON(w, http.StatusOK, b.buildCredentials(instance, existing))
		return
	}

	// Parse request
	var req BindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		b.writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON body")
		return
	}

	binding := &store.ServiceBinding{
		ID:         bindingID,
		InstanceID: instanceID,
		AppGUID:    req.AppGUID,
		Parameters: req.Parameters,
		CreatedAt:  time.Now(),
	}

	// For dedicated clusters, pre-generate credentials (they go in the deployment manifest)
	// For shared clusters, createS3Credentials will generate them via IAM API
	if instance.DeploymentName != "" {
		binding.AccessKey = generateAccessKey()
		binding.SecretKey = generateSecretKey()
	}

	// Create S3 credentials on the cluster (for shared clusters, this calls IAM API)
	if err := b.createS3Credentials(instance, binding); err != nil {
		b.writeError(w, http.StatusInternalServerError, "BindError", err.Error())
		return
	}

	if err := b.store.SaveBinding(binding); err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}

	b.writeJSON(w, http.StatusCreated, b.buildCredentials(instance, binding))
}

func (b *Broker) unbindHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	binding, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if binding == nil {
		b.writeError(w, http.StatusGone, "BindingNotFound", "Service binding not found")
		return
	}

	// Revoke S3 credentials
	if err := b.deleteS3Credentials(instance, binding); err != nil {
		log.Printf("Warning: failed to delete S3 credentials: %v", err)
	}

	if err := b.store.DeleteBinding(bindingID); err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}

	b.writeJSON(w, http.StatusOK, map[string]any{})
}

func (b *Broker) getBindingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	binding, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if binding == nil {
		b.writeError(w, http.StatusNotFound, "BindingNotFound", "Service binding not found")
		return
	}

	b.writeJSON(w, http.StatusOK, b.buildCredentials(instance, binding))
}

// Helper methods

func (b *Broker) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (b *Broker) writeError(w http.ResponseWriter, status int, errorCode, description string) {
	b.writeJSON(w, status, map[string]string{
		"error":       errorCode,
		"description": description,
	})
}

func (b *Broker) findPlan(serviceID, planID string) *config.PlanConfig {
	for _, service := range b.config.Catalog.Services {
		if service.ID == serviceID {
			for _, plan := range service.Plans {
				if plan.ID == planID {
					return &plan
				}
			}
		}
	}
	return nil
}

func (b *Broker) getDashboardURL(instance *store.ServiceInstance) string {
	// Return appropriate dashboard URL based on instance type
	if instance.DeploymentName != "" && instance.ConsoleURL != "" {
		return instance.ConsoleURL
	}
	if instance.DeploymentName != "" {
		// Generate console URL if CF domain is configured
		if b.config.CF.SystemDomain != "" {
			return fmt.Sprintf("https://seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
		}
	}
	return ""
}

func (b *Broker) buildCatalog() map[string]any {
	services := make([]map[string]any, 0, len(b.config.Catalog.Services))

	for _, svc := range b.config.Catalog.Services {
		plans := make([]map[string]any, 0, len(svc.Plans))
		for _, plan := range svc.Plans {
			planData := map[string]any{
				"id":          plan.ID,
				"name":        plan.Name,
				"description": plan.Description,
				"free":        plan.Free,
				"bindable":    true,
				"metadata": map[string]any{
					"displayName": plan.Metadata.DisplayName,
					"bullets":     plan.Metadata.Bullets,
				},
			}
			plans = append(plans, planData)
		}

		serviceData := map[string]any{
			"id":                    svc.ID,
			"name":                  svc.Name,
			"description":           svc.Description,
			"bindable":              svc.Bindable,
			"instances_retrievable": true,
			"bindings_retrievable":  true,
			"plan_updateable":       false,
			"plans":                 plans,
			"tags":                  svc.Tags,
			"metadata": map[string]any{
				"displayName":         svc.Metadata.DisplayName,
				"imageUrl":            svc.Metadata.ImageURL,
				"longDescription":     svc.Metadata.LongDescription,
				"providerDisplayName": svc.Metadata.ProviderDisplayName,
				"documentationUrl":    svc.Metadata.DocumentationURL,
				"supportUrl":          svc.Metadata.SupportURL,
			},
		}

		services = append(services, serviceData)
	}

	return map[string]any{
		"services": services,
	}
}

func (b *Broker) buildCredentials(instance *store.ServiceInstance, binding *store.ServiceBinding) map[string]any {
	var endpoint, bucket string
	var useSSL bool

	if instance.DeploymentName != "" {
		// Dedicated cluster
		endpoint = instance.S3Endpoint
		bucket = instance.BucketName
		useSSL = true
	} else {
		// Shared cluster
		endpoint = b.config.SharedCluster.S3Endpoint
		bucket = instance.BucketName
		useSSL = b.config.SharedCluster.UseSSL
	}

	// Always use the binding's credentials - they are created dynamically via IAM API
	// for shared clusters, or configured in the deployment manifest for dedicated clusters
	accessKey := binding.AccessKey
	secretKey := binding.SecretKey

	// Construct endpoint URL with appropriate protocol
	protocol := "http"
	if useSSL {
		protocol = "https"
	}
	endpointURL := fmt.Sprintf("%s://%s", protocol, endpoint)

	creds := map[string]any{
		"endpoint":     endpoint,
		"endpoint_url": endpointURL,
		"bucket":       bucket,
		"access_key":   accessKey,
		"secret_key":   secretKey,
		"region":       b.config.SharedCluster.Region,
		"use_ssl":      useSSL,
		"uri":          fmt.Sprintf("s3://%s:%s@%s/%s", accessKey, secretKey, endpoint, bucket),
	}

	// Include console URL for dedicated clusters
	if instance.DeploymentName != "" && instance.ConsoleURL != "" {
		creds["console_url"] = instance.ConsoleURL
	}

	return map[string]any{
		"credentials": creds,
	}
}

func generateAccessKey() string {
	bytes := make([]byte, 10)
	rand.Read(bytes)
	return strings.ToUpper(hex.EncodeToString(bytes))
}

func generateSecretKey() string {
	bytes := make([]byte, 20)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// Request/Response types

type ProvisionRequest struct {
	ServiceID        string         `json:"service_id"`
	PlanID           string         `json:"plan_id"`
	OrganizationGUID string         `json:"organization_guid"`
	SpaceGUID        string         `json:"space_guid"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	Context          map[string]any `json:"context,omitempty"`
}

type BindRequest struct {
	ServiceID  string         `json:"service_id"`
	PlanID     string         `json:"plan_id"`
	AppGUID    string         `json:"app_guid,omitempty"`
	BindResource map[string]any `json:"bind_resource,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Provisioning implementations

func (b *Broker) provisionSharedBucket(instance *store.ServiceInstance) error {
	if b.s3Client == nil {
		return fmt.Errorf("shared cluster not configured")
	}

	// Generate bucket name
	bucketName := fmt.Sprintf("cf-%s-%s", instance.SpaceGUID[:8], instance.ID[:8])
	instance.BucketName = bucketName

	// Create bucket
	ctx := context.Background()
	err := b.s3Client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{
		Region: b.config.SharedCluster.Region,
	})
	if err != nil {
		// Check if bucket already exists
		exists, errBucketExists := b.s3Client.BucketExists(ctx, bucketName)
		if errBucketExists != nil || !exists {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	log.Printf("Created bucket %s for instance %s", bucketName, instance.ID)
	return nil
}

func (b *Broker) deprovisionSharedBucket(instance *store.ServiceInstance) error {
	if b.s3Client == nil || instance.BucketName == "" {
		return nil
	}

	ctx := context.Background()

	// Delete all objects in bucket first
	objectsCh := b.s3Client.ListObjects(ctx, instance.BucketName, minio.ListObjectsOptions{
		Recursive: true,
	})

	for object := range objectsCh {
		if object.Err != nil {
			log.Printf("Error listing objects: %v", object.Err)
			continue
		}
		err := b.s3Client.RemoveObject(ctx, instance.BucketName, object.Key, minio.RemoveObjectOptions{})
		if err != nil {
			log.Printf("Error removing object %s: %v", object.Key, err)
		}
	}

	// Delete bucket
	if err := b.s3Client.RemoveBucket(ctx, instance.BucketName); err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	log.Printf("Deleted bucket %s for instance %s", instance.BucketName, instance.ID)
	return nil
}

func (b *Broker) createS3Credentials(instance *store.ServiceInstance, binding *store.ServiceBinding) error {
	// For dedicated clusters, credentials are managed in the deployment manifest
	if instance.DeploymentName != "" {
		log.Printf("Binding %s: Using dedicated cluster credentials for instance %s", binding.ID, instance.ID)
		return nil
	}

	// For shared clusters, use the IAM API to create dedicated credentials per binding
	if b.iamClient == nil {
		return fmt.Errorf("IAM client not initialized - cannot create per-binding credentials")
	}

	// Create an IAM user name based on the binding ID (sanitized for IAM)
	// Format: cf-binding-<first 16 chars of binding ID>
	userName := fmt.Sprintf("cf-binding-%s", binding.ID[:min(len(binding.ID), 16)])

	log.Printf("Binding %s: Creating IAM user %s via SeaweedFS IAM API", binding.ID, userName)

	// Create the IAM user first
	if err := b.iamClient.CreateUser(userName); err != nil {
		log.Printf("Binding %s: IAM CreateUser failed: %v", binding.ID, err)
		return fmt.Errorf("failed to create IAM user: %w", err)
	}

	// Create access key for the user
	accessKey, err := b.iamClient.CreateAccessKey(userName)
	if err != nil {
		log.Printf("Binding %s: IAM CreateAccessKey failed: %v", binding.ID, err)
		return fmt.Errorf("failed to create S3 credentials via IAM API: %w", err)
	}

	// Store the IAM user name and credentials
	binding.IAMUserName = userName
	binding.AccessKey = accessKey.AccessKeyID
	binding.SecretKey = accessKey.SecretAccessKey

	log.Printf("Binding %s: Created IAM credentials for user %s, access_key=%s",
		binding.ID, userName, accessKey.AccessKeyID)

	// Optionally attach a policy to restrict access to only this binding's bucket
	// Note: SeaweedFS policy support may be limited
	policyName := fmt.Sprintf("bucket-access-%s", binding.ID[:min(len(binding.ID), 8)])
	if err := b.iamClient.PutUserPolicy(userName, policyName, instance.BucketName); err != nil {
		// Policy attachment is optional - log but don't fail
		log.Printf("Warning: Could not attach bucket policy for user %s: %v", userName, err)
	}

	return nil
}

func (b *Broker) deleteS3Credentials(instance *store.ServiceInstance, binding *store.ServiceBinding) error {
	// For dedicated clusters, credentials are managed in the deployment manifest
	if instance.DeploymentName != "" {
		log.Printf("Binding %s: Dedicated cluster credentials will be removed with deployment", binding.ID)
		return nil
	}

	// For shared clusters, delete the IAM credentials
	if b.iamClient == nil || binding.IAMUserName == "" {
		log.Printf("Binding %s: No IAM credentials to delete", binding.ID)
		return nil
	}

	log.Printf("Binding %s: Deleting IAM credentials for user %s", binding.ID, binding.IAMUserName)

	// Delete the user policy first (best effort)
	policyName := fmt.Sprintf("bucket-access-%s", binding.ID[:min(len(binding.ID), 8)])
	if err := b.iamClient.DeleteUserPolicy(binding.IAMUserName, policyName); err != nil {
		log.Printf("Warning: Could not delete user policy: %v", err)
	}

	// Delete the access key
	if binding.AccessKey != "" {
		if err := b.iamClient.DeleteAccessKey(binding.IAMUserName, binding.AccessKey); err != nil {
			log.Printf("Warning: Could not delete access key %s: %v", binding.AccessKey, err)
		}
	}

	// Delete the IAM user
	if err := b.iamClient.DeleteUser(binding.IAMUserName); err != nil {
		log.Printf("Warning: Could not delete IAM user %s: %v", binding.IAMUserName, err)
	}

	log.Printf("Binding %s: Deleted IAM credentials and user %s", binding.ID, binding.IAMUserName)
	return nil
}

func (b *Broker) provisionDedicatedCluster(instance *store.ServiceInstance, plan *config.PlanConfig) {
	if b.boshClient == nil {
		instance.State = "failed"
		instance.StateMessage = "BOSH director not configured"
		b.store.SaveInstance(instance)
		return
	}

	deploymentName := fmt.Sprintf("%s-%s", b.config.BOSH.DeploymentPrefix, instance.ID[:8])
	instance.DeploymentName = deploymentName

	// Generate manifest
	manifest := b.generateDedicatedManifest(instance, plan)

	// Deploy
	task, err := b.boshClient.Deploy(manifest)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Failed to start deployment: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	instance.StateMessage = fmt.Sprintf("Deployment started, task ID: %d", task.ID)
	b.store.SaveInstance(instance)

	// Wait for deployment
	task, err = b.boshClient.WaitForTask(task.ID, 30*time.Minute)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Deployment failed: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	// Get deployment info to find endpoints
	vms, err := b.boshClient.GetDeploymentVMs(deploymentName)
	if err != nil {
		log.Printf("Warning: could not get deployment VMs: %v", err)
	} else {
		for _, vm := range vms {
			if job, ok := vm["job"].(string); ok && job == "seaweedfs-s3" {
				// Prefer DNS name over IP address for stable addressing
				if dns, ok := vm["dns"].([]any); ok && len(dns) > 0 {
					instance.S3Endpoint = fmt.Sprintf("%v:8333", dns[0])
				} else if ips, ok := vm["ips"].([]any); ok && len(ips) > 0 {
					// Fall back to IP if DNS not available
					instance.S3Endpoint = fmt.Sprintf("%v:8333", ips[0])
				}
			}
		}
	}

	// Generate admin credentials
	instance.AdminAccessKey = generateAccessKey()
	instance.AdminSecretKey = generateSecretKey()
	instance.BucketName = "default"

	// Generate console URL for dedicated cluster
	if b.config.CF.SystemDomain != "" {
		instance.ConsoleURL = fmt.Sprintf("https://seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
	}

	instance.State = "succeeded"
	instance.StateMessage = "Deployment complete"
	b.store.SaveInstance(instance)

	log.Printf("Provisioned dedicated cluster %s for instance %s", deploymentName, instance.ID)
}

func (b *Broker) deprovisionDedicatedCluster(instance *store.ServiceInstance) {
	if b.boshClient == nil || instance.DeploymentName == "" {
		b.store.DeleteInstance(instance.ID)
		return
	}

	task, err := b.boshClient.DeleteDeployment(instance.DeploymentName)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Failed to delete deployment: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	_, err = b.boshClient.WaitForTask(task.ID, 15*time.Minute)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Delete deployment failed: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	b.store.DeleteInstance(instance.ID)
	log.Printf("Deprovisioned dedicated cluster %s for instance %s", instance.DeploymentName, instance.ID)
}

func (b *Broker) generateDedicatedManifest(instance *store.ServiceInstance, plan *config.PlanConfig) []byte {
	cfg := plan.DedicatedConfig
	if cfg == nil {
		cfg = &config.DedicatedPlanConfig{
			VMType:      "default",
			DiskType:    "default",
			MasterNodes: 1,
			VolumeNodes: 3,
			FilerNodes:  1,
			Replication: "001",
			Network:     "default",
			AZs:         []string{"z1"},
		}
	}
	if cfg.Replication == "" {
		cfg.Replication = "001"
	}

	// Generate console route hostname
	consoleHostname := ""
	routeRegistrarSection := ""
	additionalReleases := ""
	if b.config.CF.SystemDomain != "" {
		consoleHostname = fmt.Sprintf("seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
		// Note: Route registration for on-demand instances requires network access to NATS
		// and the routing release to be uploaded to the BOSH director.
		// The nats-tls link must be available from the TAS deployment.
		additionalReleases = `- name: routing
  version: latest`
		routeRegistrarSection = fmt.Sprintf(`  - name: route_registrar
    release: routing
    consumes:
      nats-tls:
        from: nats-tls
        deployment: cf
    properties:
      route_registrar:
        routes:
        - name: seaweedfs-master-console
          port: 9333
          registration_interval: 20s
          uris:
          - %s`, consoleHostname)
	}

	manifest := fmt.Sprintf(`---
name: %s

releases:
- name: %s
  version: "%s"
%s

stemcells:
- alias: default
  os: %s
  version: "%s"

update:
  canaries: 1
  max_in_flight: 1
  canary_watch_time: 30000-300000
  update_watch_time: 30000-300000

instance_groups:
- name: seaweedfs-master
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-master
    release: %s
    properties:
      seaweedfs:
        master:
          port: 9333
          default_replication: "%s"
%s

- name: seaweedfs-volume
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-volume
    release: %s
    properties:
      seaweedfs:
        volume:
          master: "((seaweedfs_master_address)):9333"

- name: seaweedfs-filer
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-filer
    release: %s
    properties:
      seaweedfs:
        filer:
          master: "((seaweedfs_master_address)):9333"

- name: seaweedfs-s3
  instances: 1
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  jobs:
  - name: seaweedfs-s3
    release: %s
    properties:
      seaweedfs:
        s3:
          filer: "((seaweedfs_filer_address)):8888"
          config:
            enabled: true
            identities:
            - name: admin
              access_key: %s
              secret_key: %s
              actions:
              - Admin
              - Read
              - Write

variables:
- name: seaweedfs_master_address
  type: certificate
- name: seaweedfs_filer_address
  type: certificate
`,
		instance.DeploymentName,
		b.config.BOSH.ReleaseName,
		b.config.BOSH.ReleaseVersion,
		additionalReleases,
		b.config.BOSH.StemcellOS,
		b.config.BOSH.StemcellVersion,
		cfg.MasterNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.Replication,
		routeRegistrarSection,
		cfg.VolumeNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.FilerNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		b.config.BOSH.ReleaseName,
		instance.AdminAccessKey,
		instance.AdminSecretKey,
	)

	return []byte(manifest)
}

func toYAMLArray(items []string) string {
	if len(items) == 0 {
		return "[z1]"
	}
	return fmt.Sprintf("[%s]", strings.Join(items, ", "))
}
