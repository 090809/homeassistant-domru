package tokenmanagement

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/090809/homeassistant-domru/internal/domru/constants"
	"github.com/090809/homeassistant-domru/internal/domru/helpers"
	"github.com/090809/homeassistant-domru/internal/domru/models"
	"github.com/090809/homeassistant-domru/pkg/auth"
)

type ValidTokenProvider struct {
	Logger           *slog.Logger
	credentialsStore auth.CredentialsStore
}

func NewValidTokenProvider(credentialsStore auth.CredentialsStore) *ValidTokenProvider {
	v := &ValidTokenProvider{
		credentialsStore: credentialsStore,
		Logger:           slog.Default(),
	}
	return v
}

func (v *ValidTokenProvider) GetOperatorID() (int, error) {
	credentials, err := v.credentialsStore.LoadCredentials()
	if err != nil {
		return 0, fmt.Errorf("load credentials: %w", err)
	}

	return credentials.OperatorID, nil
}

func (v *ValidTokenProvider) GetToken() (string, error) {
	credentials, err := v.credentialsStore.LoadCredentials()
	if err != nil {
		v.Logger.With("err", err.Error()).Warn("load credentials")
		return "", fmt.Errorf("load credentials: %w", err)
	}

	return credentials.AccessToken, nil
}

func (v *ValidTokenProvider) RefreshToken() error {
	v.Logger.Debug("refreshing token...")
	credentials, err := v.credentialsStore.LoadCredentials()
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}

	var refreshTokenResponse models.AuthenticationResponse
	refreshURL := fmt.Sprintf(constants.API_REFRESH_SESSION, constants.BaseUrl)
	err = helpers.NewUpstreamRequest(refreshURL,
		helpers.WithHeader("Bearer", credentials.RefreshToken),
		helpers.WithHeader("Operator", fmt.Sprint(credentials.OperatorID)),
	).Send(http.MethodGet, &refreshTokenResponse)
	if err != nil {
		return fmt.Errorf("send request to refresh token: %w", err)
	}

	err = v.credentialsStore.SaveCredentials(auth.NewCredentialsFromAuthResponse(refreshTokenResponse))
	if err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	return nil
}
