package coordinator

import (
	"context"
	"reflect"

	"github.com/regb/workitem/internal/model"
)

// Transport carries references, not cached repository locations. Resolve any
// item references in a response through the daemon's bounded source projection.
func (c *Client) resolveResponseRepositories(ctx context.Context, result any) error {
	cache := map[string]RepositorySourceResult{}
	repoType := reflect.TypeOf(model.Repository{})
	var visit func(reflect.Value) error
	visit = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return nil
			}
			return visit(v.Elem())
		}
		if v.Type() == repoType && v.CanAddr() {
			r := v.Addr().Interface().(*model.Repository)
			if r.ID == "" {
				return nil
			}
			s, ok := cache[r.ID]
			if !ok {
				var err error
				err = c.Call(ctx, MethodRepositorySource, RepositorySourceRequest{Selector: r.ID, MetadataOnly: true}, &s)
				if err != nil {
					return err
				}
				cache[r.ID] = s
			}
			r.Name = s.Repository.Name
			r.IntegrationBranch = s.Repository.IntegrationBranch
			r.RootAtCreation = s.GitDir
			r.GitCommonDir = s.GitDir
			r.RemoteURL = ""
			if s.Repository.Upstream != nil {
				r.RemoteURL = s.Repository.Upstream.URL
			}
			return nil
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Field(i).CanInterface() {
					if err := visit(v.Field(i)); err != nil {
						return err
					}
				}
			}
		case reflect.Slice:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return nil
			}
			for i := 0; i < v.Len(); i++ {
				if err := visit(v.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(reflect.ValueOf(result))
}
