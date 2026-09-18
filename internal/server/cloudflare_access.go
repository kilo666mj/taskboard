package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/store"
)

const (
	cloudflareAccessJWTHeader = "Cf-Access-Jwt-Assertion"
	maxAccessTokenBytes       = 16 << 10
)

var errInvalidCloudflareAccess = errors.New("invalid Cloudflare Access assertion")

type cloudflareAccessIdentity struct {
	Subject string
	Email   string
	Groups  []string
	Service bool
}

type cloudflareAccessVerifier interface {
	Verify(context.Context, string) (cloudflareAccessIdentity, error)
}

type remoteCloudflareAccessVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func (v remoteCloudflareAccessVerifier) Verify(ctx context.Context, raw string) (cloudflareAccessIdentity, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return cloudflareAccessIdentity{}, errInvalidCloudflareAccess
	}
	var claims struct {
		Email      string   `json:"email"`
		Type       string   `json:"type"`
		CommonName string   `json:"common_name"`
		Groups     []string `json:"groups"`
		Custom     struct {
			Groups []string `json:"groups"`
		} `json:"custom"`
	}
	if err := token.Claims(&claims); err != nil || claims.Type != "app" {
		return cloudflareAccessIdentity{}, errInvalidCloudflareAccess
	}
	groups := append([]string(nil), claims.Groups...)
	for _, group := range claims.Custom.Groups {
		if !slices.Contains(groups, group) {
			groups = append(groups, group)
		}
	}
	principal, serviceIdentity, err := cloudflareAccessPrincipal(token.Subject, claims.Email, claims.CommonName)
	if err != nil {
		return cloudflareAccessIdentity{}, errInvalidCloudflareAccess
	}
	return cloudflareAccessIdentity{Subject: principal, Email: claims.Email, Groups: groups, Service: serviceIdentity}, nil
}

type cloudflareAccess struct {
	verifier        cloudflareAccessVerifier
	allowedSubjects []string
	allowedEmails   []string
	allowedGroups   []string
}

func newCloudflareAccess(cfg config.Config) *cloudflareAccess {
	if !cfg.CloudflareAccessEnabled() {
		return nil
	}
	keys := oidc.NewRemoteKeySet(context.Background(), cfg.CFAccessTeamDomain+"/cdn-cgi/access/certs")
	verifier := oidc.NewVerifier(cfg.CFAccessTeamDomain, keys, &oidc.Config{ClientID: cfg.CFAccessAudience})
	return &cloudflareAccess{
		verifier:        remoteCloudflareAccessVerifier{verifier: verifier},
		allowedSubjects: append([]string(nil), cfg.CFAccessSubjects...),
		allowedEmails:   append([]string(nil), cfg.CFAccessEmails...),
		allowedGroups:   append([]string(nil), cfg.CFAccessGroups...),
	}
}

func (a *cloudflareAccess) identity(r *http.Request) (store.BrowserIdentity, error) {
	values := r.Header.Values(cloudflareAccessJWTHeader)
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > maxAccessTokenBytes || strings.TrimSpace(values[0]) != values[0] {
		return store.BrowserIdentity{}, errInvalidCloudflareAccess
	}
	identity, err := a.verifier.Verify(r.Context(), values[0])
	if err != nil || !a.allowed(identity) {
		return store.BrowserIdentity{}, errInvalidCloudflareAccess
	}
	return store.BrowserIdentity{
		Subject: "cloudflare_access:" + identity.Subject,
		Email:   identity.Email,
		Groups:  append([]string(nil), identity.Groups...),
		Service: identity.Service,
	}, nil
}

func (a *cloudflareAccess) allowed(identity cloudflareAccessIdentity) bool {
	if len(a.allowedSubjects) == 0 && len(a.allowedEmails) == 0 && len(a.allowedGroups) == 0 {
		return true
	}
	if slices.Contains(a.allowedSubjects, identity.Subject) || slices.Contains(a.allowedEmails, identity.Email) {
		return true
	}
	for _, group := range identity.Groups {
		if slices.Contains(a.allowedGroups, group) {
			return true
		}
	}
	return false
}

func safeAccessClaim(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func cloudflareAccessPrincipal(subject, email, commonName string) (string, bool, error) {
	if subject != "" {
		if safeAccessClaim(subject) && safeAccessClaim(email) {
			return subject, false, nil
		}
		return "", false, errInvalidCloudflareAccess
	}
	if email == "" && safeAccessClaim(commonName) {
		return "service_token:" + commonName, true, nil
	}
	return "", false, errInvalidCloudflareAccess
}
