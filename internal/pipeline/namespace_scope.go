package pipeline

import (
	"context"
	"net/http"
	"slices"
)

type allowedNamespacesKey struct{}

// WithAllowedNamespaces restricts pipeline operations on ctx to namespaces.
// Empty slice leaves ctx unrestricted.
func WithAllowedNamespaces(ctx context.Context, namespaces []string) context.Context {
	if len(namespaces) == 0 {
		return ctx
	}
	return context.WithValue(ctx, allowedNamespacesKey{}, slices.Clone(namespaces))
}

func allowedNamespaces(ctx context.Context) []string {
	v, _ := ctx.Value(allowedNamespacesKey{}).([]string)
	return v
}

func checkNamespace(ctx context.Context, ns string) error {
	allowed := allowedNamespaces(ctx)
	if len(allowed) == 0 || slices.Contains(allowed, ns) {
		return nil
	}
	return apiErr(http.StatusForbidden, CodeNamespaceForbidden, "api key is not permitted to access namespace %q", ns)
}
