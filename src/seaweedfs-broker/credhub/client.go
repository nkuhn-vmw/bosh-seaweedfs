package credhub

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client provides methods for interacting with a CredHub server.
type Client struct {
	apiURL       string
	clientID     string
	clientSecret string
	caCert       string
	httpClient   *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewClient creates a new CredHub client. If caCert is non-empty, it is added
// to the TLS root CA pool used for HTTPS connections to CredHub.
func NewClient(apiURL, clientID, clientSecret, caCert string) (*Client, error) {
	tlsConfig := &tls.Config{}

	if caCert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caCert)) {
			return nil, fmt.Errorf("credhub: failed to parse CA certificate")
		}
		tlsConfig.RootCAs = pool
	}

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	return &Client{
		apiURL:       strings.TrimRight(apiURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		caCert:       caCert,
		httpClient:   httpClient,
	}, nil
}

// authenticate performs an OAuth2 client_credentials grant against the CredHub
// UAA token endpoint and caches the resulting access token.
func (c *Client) authenticate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return cached token if still valid (with a small buffer).
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-30*time.Second)) {
		return nil
	}

	tokenURL := c.apiURL + "/oauth/token"

	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}

	resp, err := c.httpClient.PostForm(tokenURL, data)
	if err != nil {
		return fmt.Errorf("credhub: token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("credhub: failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credhub: token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("credhub: failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return fmt.Errorf("credhub: empty access token in response")
	}

	c.accessToken = tokenResp.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return nil
}

// SetJSON creates or updates a JSON credential at the given path in CredHub.
func (c *Client) SetJSON(path string, value map[string]interface{}) error {
	if err := c.authenticate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"name":  path,
		"type":  "json",
		"value": value,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("credhub: failed to marshal credential: %w", err)
	}

	reqURL := c.apiURL + "/api/v1/data"
	req, err := http.NewRequest(http.MethodPut, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("credhub: failed to create request: %w", err)
	}

	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("credhub: set request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("credhub: set credential returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Delete removes a credential by name from CredHub.
func (c *Client) Delete(path string) error {
	if err := c.authenticate(); err != nil {
		return err
	}

	reqURL := c.apiURL + "/api/v1/data?name=" + url.QueryEscape(path)
	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return fmt.Errorf("credhub: failed to create delete request: %w", err)
	}

	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("credhub: delete request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("credhub: delete credential returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
