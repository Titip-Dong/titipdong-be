package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// SecurityScheme is the OpenAPI security scheme name for bearer access tokens.
const SecurityScheme = "bearerAuth"

// Security marks an operation as requiring a valid access token. With roles,
// only those roles may call it; other roles get 403.
//
//	huma.Operation{..., Security: auth.Security(auth.RoleAdmin)}
func Security(roles ...Role) []map[string][]string {
	scopes := make([]string, len(roles))
	for i, r := range roles {
		scopes[i] = string(r)
	}
	return []map[string][]string{{SecurityScheme: scopes}}
}

type principalKey struct{}

// PrincipalFrom returns the caller authenticated by Middleware. It is only set
// for operations declared with Security.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Middleware enforces Security on operations. Register it with
// api.UseMiddleware before registering any operation, because Huma binds
// middleware when an operation is registered.
func Middleware(api huma.API, svc Service) func(huma.Context, func(huma.Context)) {
	api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SecurityScheme: {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}

	return func(ctx huma.Context, next func(huma.Context)) {
		roles, protected := requiredRoles(ctx.Operation())
		if !protected {
			next(ctx)
			return
		}
		token, ok := strings.CutPrefix(ctx.Header("Authorization"), "Bearer ")
		if !ok || token == "" {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "Provide a valid access token.")
			return
		}
		p, err := svc.Authenticate(ctx.Context(), token)
		if err != nil {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "Provide a valid access token.")
			return
		}
		if len(roles) > 0 && !slices.Contains(roles, string(p.Role)) {
			_ = huma.WriteErr(api, ctx, http.StatusForbidden, "Your role cannot access this resource.")
			return
		}
		next(huma.WithValue(ctx, principalKey{}, p))
	}
}

func requiredRoles(op *huma.Operation) ([]string, bool) {
	for _, req := range op.Security {
		if roles, ok := req[SecurityScheme]; ok {
			return roles, true
		}
	}
	return nil, false
}
