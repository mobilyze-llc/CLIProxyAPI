package api

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const museCodeModelsPath = "/muse-code/models"

func (s *Server) museCodeModels(c *gin.Context) {
	credential, baseURL, token := s.museCodeCredential()
	if credential == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no usable Meta credential for Muse Code models"})
		return
	}

	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		log.WithField("auth_id", credential.ID).Warn("muse code models: invalid Meta base URL")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid Meta base URL"})
		return
	}
	target := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: museCodeModelsPath}).String()

	ctx := c.Request.Context()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errReq != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to build Muse Code models request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, errDo := helps.NewProxyAwareHTTPClient(ctx, s.getConfig(), credential, 0).Do(req)
	if errDo != nil {
		log.WithField("auth_id", credential.ID).Warnf("muse code models: upstream request failed: %v", errDo)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Muse Code models upstream request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse code models: close upstream body: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		log.WithField("auth_id", credential.ID).Warnf("muse code models: read upstream body: %v", errRead)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read Muse Code models response"})
		return
	}
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func (s *Server) museCodeCredential() (*auth.Auth, string, string) {
	if s.handlers == nil || s.handlers.AuthManager == nil {
		return nil, "", ""
	}
	var chosen *auth.Auth
	var chosenBaseURL, chosenToken string
	for _, candidate := range s.handlers.AuthManager.List() {
		if !strings.EqualFold(candidate.Provider, "meta") || candidate.Disabled || candidate.Status == auth.StatusDisabled {
			continue
		}
		baseURL, token := helps.MetaCreds(candidate)
		if token == "" {
			continue
		}
		if chosen == nil || candidate.ID < chosen.ID {
			chosen, chosenBaseURL, chosenToken = candidate, baseURL, token
		}
	}
	return chosen, chosenBaseURL, chosenToken
}
