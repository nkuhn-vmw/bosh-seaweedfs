package bosh

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudfoundry/seaweedfs-broker/config"
)

// Client is a BOSH director API client
type Client struct {
	directorURL string
	httpClient  *http.Client
	token       string
	tokenExpiry time.Time
	clientID    string
	clientSecret string
}

// DeploymentManifest represents a BOSH deployment manifest
type DeploymentManifest struct {
	Name           string          `yaml:"name"`
	Releases       []Release       `yaml:"releases"`
	Stemcells      []Stemcell      `yaml:"stemcells"`
	Update         Update          `yaml:"update"`
	InstanceGroups []InstanceGroup `yaml:"instance_groups"`
	Variables      []Variable      `yaml:"variables,omitempty"`
}

// Release represents a BOSH release
type Release struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// Stemcell represents a BOSH stemcell
type Stemcell struct {
	Alias   string `yaml:"alias"`
	OS      string `yaml:"os"`
	Version string `yaml:"version"`
}

// Update represents deployment update settings
type Update struct {
	Canaries        int    `yaml:"canaries"`
	MaxInFlight     int    `yaml:"max_in_flight"`
	CanaryWatchTime string `yaml:"canary_watch_time"`
	UpdateWatchTime string `yaml:"update_watch_time"`
}

// InstanceGroup represents a BOSH instance group
type InstanceGroup struct {
	Name               string            `yaml:"name"`
	Instances          int               `yaml:"instances"`
	VMType             string            `yaml:"vm_type"`
	Stemcell           string            `yaml:"stemcell"`
	AZs                []string          `yaml:"azs"`
	Networks           []Network         `yaml:"networks"`
	Jobs               []Job             `yaml:"jobs"`
	PersistentDiskType string            `yaml:"persistent_disk_type,omitempty"`
	Properties         map[string]any    `yaml:"properties,omitempty"`
}

// Network represents a network configuration
type Network struct {
	Name      string   `yaml:"name"`
	StaticIPs []string `yaml:"static_ips,omitempty"`
}

// Job represents a BOSH job
type Job struct {
	Name       string         `yaml:"name"`
	Release    string         `yaml:"release"`
	Properties map[string]any `yaml:"properties,omitempty"`
}

// Variable represents a BOSH variable
type Variable struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	Options map[string]string `yaml:"options,omitempty"`
}

// Task represents a BOSH task
type Task struct {
	ID          int    `json:"id"`
	State       string `json:"state"`
	Description string `json:"description"`
	Result      string `json:"result"`
	Timestamp   int64  `json:"timestamp"`
}

// Deployment represents a BOSH deployment
type Deployment struct {
	Name        string   `json:"name"`
	CloudConfig string   `json:"cloud_config"`
	Releases    []Release `json:"releases"`
	Stemcells   []Stemcell `json:"stemcells"`
}

// NewClient creates a new BOSH client
func NewClient(cfg *config.BOSHConfig) (*Client, error) {
	// Create TLS config with CA cert
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if cfg.DirectorCACert != "" {
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM([]byte(cfg.DirectorCACert)) {
			return nil, fmt.Errorf("failed to parse BOSH director CA cert")
		}
		tlsConfig.RootCAs = caCertPool
	} else {
		tlsConfig.InsecureSkipVerify = true
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 30 * time.Second,
	}

	return &Client{
		directorURL:  strings.TrimSuffix(cfg.DirectorURL, "/"),
		httpClient:   httpClient,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
	}, nil
}

// authenticate gets an OAuth token from UAA
func (c *Client) authenticate() error {
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return nil
	}

	// Get UAA URL from BOSH director info
	infoResp, err := c.httpClient.Get(c.directorURL + "/info")
	if err != nil {
		return fmt.Errorf("failed to get director info: %w", err)
	}
	defer infoResp.Body.Close()

	var info struct {
		UserAuthentication struct {
			Type    string `json:"type"`
			Options struct {
				URL string `json:"url"`
			} `json:"options"`
		} `json:"user_authentication"`
	}
	if err := json.NewDecoder(infoResp.Body).Decode(&info); err != nil {
		return fmt.Errorf("failed to decode director info: %w", err)
	}

	uaaURL := info.UserAuthentication.Options.URL

	// Request token from UAA
	tokenReq, err := http.NewRequest("POST", uaaURL+"/oauth/token",
		strings.NewReader(fmt.Sprintf(
			"grant_type=client_credentials&client_id=%s&client_secret=%s",
			c.clientID, c.clientSecret,
		)))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")

	tokenResp, err := c.httpClient.Do(tokenReq)
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		return fmt.Errorf("failed to get token: %s - %s", tokenResp.Status, string(body))
	}

	var tokenData struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	c.token = tokenData.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenData.ExpiresIn-60) * time.Second)

	return nil
}

// doRequest performs an authenticated request to BOSH director
func (c *Client) doRequest(method, path string, body io.Reader) (*http.Response, error) {
	if err := c.authenticate(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(method, c.directorURL+path, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	return c.httpClient.Do(req)
}

// Deploy creates or updates a deployment
func (c *Client) Deploy(manifest []byte) (*Task, error) {
	resp, err := c.doRequest("POST", "/deployments", bytes.NewReader(manifest))
	if err != nil {
		return nil, fmt.Errorf("failed to deploy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("deploy failed: %s - %s", resp.Status, string(body))
	}

	// Extract task ID from redirect location
	location := resp.Header.Get("Location")
	var taskID int
	fmt.Sscanf(location, "/tasks/%d", &taskID)

	return c.GetTask(taskID)
}

// DeleteDeployment deletes a deployment
func (c *Client) DeleteDeployment(name string) (*Task, error) {
	resp, err := c.doRequest("DELETE", "/deployments/"+name+"?force=true", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to delete deployment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("delete deployment failed: %s - %s", resp.Status, string(body))
	}

	location := resp.Header.Get("Location")
	var taskID int
	fmt.Sscanf(location, "/tasks/%d", &taskID)

	return c.GetTask(taskID)
}

// GetDeployment gets deployment details
func (c *Client) GetDeployment(name string) (*Deployment, error) {
	resp, err := c.doRequest("GET", "/deployments/"+name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get deployment failed: %s - %s", resp.Status, string(body))
	}

	var deployment Deployment
	if err := json.NewDecoder(resp.Body).Decode(&deployment); err != nil {
		return nil, fmt.Errorf("failed to decode deployment: %w", err)
	}

	return &deployment, nil
}

// GetTask gets task details
func (c *Client) GetTask(taskID int) (*Task, error) {
	resp, err := c.doRequest("GET", fmt.Sprintf("/tasks/%d", taskID), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get task failed: %s - %s", resp.Status, string(body))
	}

	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("failed to decode task: %w", err)
	}

	return &task, nil
}

// WaitForTask waits for a task to complete
func (c *Client) WaitForTask(taskID int, timeout time.Duration) (*Task, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		task, err := c.GetTask(taskID)
		if err != nil {
			return nil, err
		}

		switch task.State {
		case "done":
			return task, nil
		case "error", "cancelled", "timeout":
			return task, fmt.Errorf("task %d failed: %s - %s", taskID, task.State, task.Result)
		}

		time.Sleep(5 * time.Second)
	}

	return nil, fmt.Errorf("task %d timed out", taskID)
}

// GetDeploymentVMs gets VMs for a deployment
func (c *Client) GetDeploymentVMs(deploymentName string) ([]map[string]any, error) {
	resp, err := c.doRequest("GET", "/deployments/"+deploymentName+"/vms?format=full", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get VMs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound {
		// This returns a task, wait for it
		location := resp.Header.Get("Location")
		var taskID int
		fmt.Sscanf(location, "/tasks/%d", &taskID)

		task, err := c.WaitForTask(taskID, 2*time.Minute)
		if err != nil {
			return nil, err
		}

		// Get task output
		outputResp, err := c.doRequest("GET", fmt.Sprintf("/tasks/%d/output?type=result", task.ID), nil)
		if err != nil {
			return nil, err
		}
		defer outputResp.Body.Close()

		var vms []map[string]any
		body, _ := io.ReadAll(outputResp.Body)
		lines := strings.Split(string(body), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var vm map[string]any
			if err := json.Unmarshal([]byte(line), &vm); err == nil {
				vms = append(vms, vm)
			}
		}
		return vms, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get VMs failed: %s - %s", resp.Status, string(body))
	}

	var vms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&vms); err != nil {
		return nil, fmt.Errorf("failed to decode VMs: %w", err)
	}

	return vms, nil
}
