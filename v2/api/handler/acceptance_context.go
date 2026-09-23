package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/auth"
	"norn/v2/api/store"
)

const maxAccessTokenLineageDepth = 64

var errOperationActorUnverified = errors.New("stable operation actor could not be verified")

// verifiedOperationActor is derived only from authentication provenance that
// the control plane has verified. Display labels and rotating credential IDs
// are intentionally not used as actor identity.
type verifiedOperationActor struct {
	Issuer       string
	Subject      string
	CredentialID string
	DeviceID     string
	Source       string
	Scopes       []string
}

type operationAcceptanceRequestContext struct {
	ReceiptID string
	RequestID string
	Actor     verifiedOperationActor
	ActorErr  error
}

type operationAcceptanceContextKey struct{}

func withOperationAcceptanceRequestContext(r *http.Request, value operationAcceptanceRequestContext) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), operationAcceptanceContextKey{}, value))
}

func operationAcceptanceRequestContextFromRequest(r *http.Request) (operationAcceptanceRequestContext, bool) {
	value, ok := r.Context().Value(operationAcceptanceContextKey{}).(operationAcceptanceRequestContext)
	return value, ok
}

func (h *Handler) buildOperationAcceptanceRequestContext(ctx context.Context, r *http.Request, receiptID, requestID string) operationAcceptanceRequestContext {
	value := operationAcceptanceRequestContext{ReceiptID: receiptID, RequestID: requestID}
	if h == nil || h.operationStore == nil {
		value.ActorErr = fmt.Errorf("%w: operation store is unavailable", errOperationActorUnverified)
		return value
	}
	authority, err := h.operationStore.Authority(ctx)
	if err != nil {
		value.ActorErr = fmt.Errorf("%w: %v", errOperationActorUnverified, err)
		return value
	}
	value.Actor, value.ActorErr = h.resolveVerifiedOperationActor(ctx, r, authority)
	return value
}

type accessTokenLineageResolver interface {
	RootAccessToken(context.Context, string) (string, error)
}

type postgresAccessTokenLineageResolver struct {
	db *store.DB
}

// RootAccessToken follows only durable rotated_from links. A missing parent,
// cycle, or unreasonably deep chain is ambiguous and therefore fails closed.
func (r postgresAccessTokenLineageResolver) RootAccessToken(ctx context.Context, current string) (string, error) {
	if r.db == nil || r.db.Pool == nil || strings.TrimSpace(current) == "" {
		return "", fmt.Errorf("%w: token lineage store is unavailable", errOperationActorUnverified)
	}
	return resolveRootAccessToken(ctx, current, func(ctx context.Context, tokenID string) (string, error) {
		var parent string
		err := r.db.Pool.QueryRow(ctx, `SELECT rotated_from FROM access_tokens WHERE jti=$1`, tokenID).Scan(&parent)
		return parent, err
	})
}

func resolveRootAccessToken(ctx context.Context, current string, parentOf func(context.Context, string) (string, error)) (string, error) {
	seen := make(map[string]struct{}, 8)
	for depth := 0; depth < maxAccessTokenLineageDepth; depth++ {
		current = strings.TrimSpace(current)
		if current == "" {
			return "", fmt.Errorf("%w: token lineage contains an empty identifier", errOperationActorUnverified)
		}
		if _, exists := seen[current]; exists {
			return "", fmt.Errorf("%w: token lineage contains a cycle", errOperationActorUnverified)
		}
		seen[current] = struct{}{}
		parent, err := parentOf(ctx, current)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: token lineage is incomplete", errOperationActorUnverified)
		}
		if err != nil {
			return "", fmt.Errorf("%w: token lineage lookup failed: %v", errOperationActorUnverified, err)
		}
		parent = strings.TrimSpace(parent)
		if parent == "" {
			return current, nil
		}
		current = parent
	}
	return "", fmt.Errorf("%w: token lineage exceeds %d links", errOperationActorUnverified, maxAccessTokenLineageDepth)
}

func (h *Handler) resolveVerifiedOperationActor(ctx context.Context, r *http.Request, authority string) (verifiedOperationActor, error) {
	authority = strings.TrimSpace(authority)
	if authority == "" {
		return verifiedOperationActor{}, fmt.Errorf("%w: control authority is unavailable", errOperationActorUnverified)
	}
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok {
		return verifiedOperationActor{}, fmt.Errorf("%w: authenticated principal is unavailable", errOperationActorUnverified)
	}
	actor := verifiedOperationActor{
		CredentialID: strings.TrimSpace(principal.TokenID),
		DeviceID:     strings.TrimSpace(principal.DeviceID),
		Scopes:       append([]string{}, principal.Scopes...),
	}
	sort.Strings(actor.Scopes)

	if claims, present := auth.CFAccessClaimsFromRequest(r); present {
		team := ""
		if h != nil && h.cfg != nil {
			team = strings.ToLower(strings.TrimSpace(h.cfg.CFAccessTeamDomain))
		}
		subject := strings.TrimSpace(claims.Subject)
		if principal.Source != AccessPrincipalSourceCloudflareAccess || team == "" || subject == "" {
			return verifiedOperationActor{}, fmt.Errorf("%w: Cloudflare Access namespace or subject is unavailable", errOperationActorUnverified)
		}
		actor.Issuer = "https://" + strings.TrimSuffix(team, "/")
		actor.Subject = subject
		actor.Source = string(AccessPrincipalSourceCloudflareAccess)
		return actor, nil
	}

	if principal.CI != nil {
		ci := principal.CI
		if principal.Source != AccessPrincipalSourceManagedToken || ci.Provider != "github-actions" ||
			strings.TrimSpace(ci.RepositoryOwnerID) == "" || strings.TrimSpace(ci.RepositoryID) == "" ||
			strings.TrimSpace(ci.RunID) == "" || strings.TrimSpace(ci.RunAttempt) == "" {
			return verifiedOperationActor{}, fmt.Errorf("%w: GitHub Actions identity is incomplete", errOperationActorUnverified)
		}
		actor.Issuer = githubActionsOIDCIssuer
		actor.Subject = strings.Join([]string{ci.RepositoryOwnerID, ci.RepositoryID, ci.RunID, ci.RunAttempt}, ":")
		actor.Source = "github-actions"
		return actor, nil
	}

	if actor.DeviceID != "" {
		if principal.Source != AccessPrincipalSourceManagedToken {
			return verifiedOperationActor{}, fmt.Errorf("%w: device identity lacks managed credential provenance", errOperationActorUnverified)
		}
		actor.Issuer = authority + "/device"
		actor.Subject = actor.DeviceID
		actor.Source = "norn-device"
		return actor, nil
	}

	if principal.Source == AccessPrincipalSourceSharedAPI {
		actor.Issuer = authority + "/shared-api"
		actor.Subject = "control-plane-service"
		actor.Source = string(AccessPrincipalSourceSharedAPI)
		return actor, nil
	}

	if principal.Source == AccessPrincipalSourceManagedToken {
		if actor.CredentialID == "" || h == nil || h.accessTokenLineage == nil {
			return verifiedOperationActor{}, fmt.Errorf("%w: managed token lineage is unavailable", errOperationActorUnverified)
		}
		root, err := h.accessTokenLineage.RootAccessToken(ctx, actor.CredentialID)
		if err != nil {
			return verifiedOperationActor{}, err
		}
		actor.Issuer = authority + "/token-lineage"
		actor.Subject = root
		actor.Source = string(AccessPrincipalSourceManagedToken)
		return actor, nil
	}

	return verifiedOperationActor{}, fmt.Errorf("%w: unmanaged legacy credentials have no stable provenance", errOperationActorUnverified)
}
