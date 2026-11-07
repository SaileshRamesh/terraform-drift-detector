// Package cloud defines the plugin contract every cloud resource type
// implements, and the registry the scanner looks handlers up in.
package cloud

import (
	"context"
	"sort"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

// Handler maps one Terraform resource type to its cloud API. It is the
// "Resource Extractor" on both sides of the pipeline: FromState builds the
// expected model, FetchActual builds the actual model, and both must emit the
// same attribute keys and value types so they can be compared directly.
type Handler interface {
	Provider() string
	Type() string

	// FromState normalizes the attributes of one Terraform state instance.
	// The handler sets ID, Attributes and Tags, and may set Region.
	FromState(attrs map[string]any) (*model.Resource, error)

	// FetchActual looks up ids in a single region using as few API calls as
	// possible. Ids that no longer exist are omitted from the result.
	FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error)
}

// Lister is implemented by handlers that can enumerate every resource of
// their type in a region, which enables UNMANAGED detection. Implementations
// should leave out provider-created defaults (default VPC, default security
// groups...) that Terraform rarely manages.
type Lister interface {
	ListIDs(ctx context.Context, region string) ([]string, error)
}

type Registry struct {
	handlers map[string]Handler
}

func NewRegistry(handlers ...Handler) *Registry {
	r := &Registry{handlers: make(map[string]Handler)}
	for _, h := range handlers {
		r.Register(h)
	}
	return r
}

func (r *Registry) Register(h Handler) { r.handlers[h.Type()] = h }

func (r *Registry) Get(tfType string) (Handler, bool) {
	h, ok := r.handlers[tfType]
	return h, ok
}

// Types returns the supported Terraform resource types, sorted.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
