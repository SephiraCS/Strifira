package mailoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/secrets"
	"github.com/oracle/oci-go-sdk/v65/vault"
)

// VaultSource discovers the compartment/region from OCI IMDSv2 and signs OCI
// requests with the instance principal. Vendor credentials are stored once in
// Vault, never copied to environment variables or a product settings form.
type VaultSource struct{}

func (VaultSource) Credentials(ctx context.Context, provider string) (Credentials, error) {
	if provider != "google" && provider != "microsoft" {
		return Credentials{}, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Probe IMDS with a short deadline before SDK initialization, including on
	// non-OCI development hosts. Do not use the environment's HTTP proxy.
	httpClient := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/opc/v2/instance/", nil)
	req.Header.Set("Authorization", "Bearer Oracle")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Credentials{}, ErrRegistration
	}
	var instance struct {
		CompartmentID string `json:"compartmentId"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&instance)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || err != nil || instance.CompartmentID == "" {
		return Credentials{}, ErrRegistration
	}
	config, err := auth.InstancePrincipalConfigurationProviderWithCustomClient(func(common.HTTPRequestDispatcher) (common.HTTPRequestDispatcher, error) {
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}, CheckRedirect: httpClient.CheckRedirect}, nil
	})
	if err != nil {
		return Credentials{}, ErrRegistration
	}
	listing, err := vault.NewVaultsClientWithConfigurationProvider(config)
	if err != nil {
		return Credentials{}, ErrRegistration
	}
	listing.HTTPClient = httpClient
	retrieval, err := secrets.NewSecretsClientWithConfigurationProvider(config)
	if err != nil {
		return Credentials{}, ErrRegistration
	}
	retrieval.HTTPClient = httpClient
	return readVaultCredentials(ctx, instance.CompartmentID, provider, listing, retrieval)
}

type vaultListing interface {
	ListSecrets(context.Context, vault.ListSecretsRequest) (vault.ListSecretsResponse, error)
}

type vaultRetrieval interface {
	GetSecretBundle(context.Context, secrets.GetSecretBundleRequest) (secrets.GetSecretBundleResponse, error)
}

func readVaultCredentials(ctx context.Context, compartment, provider string, listing vaultListing, retrieval vaultRetrieval) (Credentials, error) {
	var id *string
	var page *string
	for {
		result, err := listing.ListSecrets(ctx, vault.ListSecretsRequest{CompartmentId: &compartment, Name: common.String("strifira-mail-oauth-" + provider), LifecycleState: vault.SecretSummaryLifecycleStateActive, Page: page})
		if err != nil {
			return Credentials{}, ErrRegistration
		}
		for _, secret := range result.Items {
			if id != nil || secret.Id == nil {
				return Credentials{}, ErrRegistration
			}
			id = secret.Id
		}
		page = result.OpcNextPage
		if page == nil || *page == "" {
			break
		}
	}
	if id == nil {
		return Credentials{}, ErrRegistration
	}
	bundle, err := retrieval.GetSecretBundle(ctx, secrets.GetSecretBundleRequest{SecretId: id, Stage: secrets.GetSecretBundleStageCurrent})
	if err != nil {
		return Credentials{}, ErrRegistration
	}
	content, ok := bundle.SecretBundleContent.(secrets.Base64SecretBundleContentDetails)
	if !ok || content.Content == nil {
		return Credentials{}, ErrRegistration
	}
	return decodeCredentials(*content.Content)
}

func decodeCredentials(encoded string) (Credentials, error) {
	var credentials Credentials
	if len(encoded) > 16<<10 {
		return credentials, ErrRegistration
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(data, &credentials) != nil || credentials.ClientID == "" || credentials.ClientSecret == "" {
		return Credentials{}, ErrRegistration
	}
	return credentials, nil
}
