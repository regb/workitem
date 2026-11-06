package daemonapi

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/types/known/structpb"
)

func profile(p model.AgentProfile) *v1.AgentProfile {
	config, _ := structpb.NewStruct(p.Config)
	return &v1.AgentProfile{Name: p.Name, Driver: p.Driver, Label: p.Label, Config: config}
}
func (s *Server) ListAgentProfiles(ctx context.Context, req *connect.Request[v1.ListAgentProfilesRequest]) (*connect.Response[v1.ListAgentProfilesResponse], error) {
	value, err := s.Local.AgentProfiles(ctx)
	out := &v1.ListAgentProfilesResponse{DefaultProfile: value.Default, Revision: uint32(value.Revision), Profiles: []*v1.AgentProfile{}}
	for _, p := range value.Profiles {
		row := profile(p)
		row.Users = uint32(s.App.AgentProfileUsers(p.Name))
		if apitransport.Caller(ctx) == apitransport.Agent {
			row.Config = nil
		}
		out.Profiles = append(out.Profiles, row)
	}
	return response(out, err)
}
func (s *Server) profilesAt(ctx context.Context, expected uint32) (model.AgentProfiles, error) {
	current, err := s.Local.AgentProfiles(ctx)
	if err != nil {
		return current, err
	}
	if current.Revision != uint64(expected) {
		return current, aborted("agent profiles revision changed")
	}
	if current.Revision >= uint64(^uint32(0)) {
		return current, connect.NewError(connect.CodeResourceExhausted, errors.New("agent profiles revision exhausted"))
	}
	return current, nil
}
func expectedRevision(expected uint32) *uint64 { v := uint64(expected); return &v }
func (s *Server) PutAgentProfile(ctx context.Context, req *connect.Request[v1.PutAgentProfileRequest]) (*connect.Response[v1.PutAgentProfileResponse], error) {
	if req.Msg.Profile == nil {
		return nil, invalid("profile is required")
	}
	current, err := s.profilesAt(ctx, req.Msg.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	p := req.Msg.Profile
	value := model.AgentProfile{Name: p.Name, Driver: p.Driver, Label: p.Label}
	if p.Config != nil {
		value.Config = p.Config.AsMap()
	}
	if err := s.App.ValidateAgentProfile(ctx, value); err != nil {
		return nil, rpcError(err)
	}
	if existing, found := current.Find(value.Name); found {
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(value)
		if string(a) == string(b) {
			return response(&v1.PutAgentProfileResponse{Profile: profile(existing), Revision: uint32(current.Revision)}, nil)
		}
	}
	result, err := s.Local.ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{ExpectedRevision: expectedRevision(req.Msg.ExpectedRevision), Put: &value})
	if err == nil {
		s.Core.Database().NotifyAll()
	}
	return response(&v1.PutAgentProfileResponse{Profile: profile(value), Revision: uint32(result.Revision)}, err)
}
func (s *Server) DeleteAgentProfile(ctx context.Context, req *connect.Request[v1.DeleteAgentProfileRequest]) (*connect.Response[v1.DeleteAgentProfileResponse], error) {
	if req.Msg.Name == "" {
		return nil, invalid("name is required")
	}
	current, err := s.profilesAt(ctx, req.Msg.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	if current.Default == req.Msg.Name {
		return nil, precondition("the default agent profile cannot be deleted")
	}
	if _, found := current.Find(req.Msg.Name); !found {
		return response(&v1.DeleteAgentProfileResponse{Revision: uint32(current.Revision)}, nil)
	}
	value, err := s.Local.ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{ExpectedRevision: expectedRevision(req.Msg.ExpectedRevision), Remove: req.Msg.Name})
	if err == nil {
		s.Core.Database().NotifyAll()
	}
	return response(&v1.DeleteAgentProfileResponse{Revision: uint32(value.Revision)}, err)
}
func (s *Server) SetDefaultAgentProfile(ctx context.Context, req *connect.Request[v1.SetDefaultAgentProfileRequest]) (*connect.Response[v1.SetDefaultAgentProfileResponse], error) {
	current, err := s.profilesAt(ctx, req.Msg.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	if _, found := current.Find(req.Msg.Name); !found {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("agent profile not found"))
	}
	if current.Default == req.Msg.Name {
		return response(&v1.SetDefaultAgentProfileResponse{Revision: uint32(current.Revision)}, nil)
	}
	value, err := s.Local.ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{ExpectedRevision: expectedRevision(req.Msg.ExpectedRevision), Default: req.Msg.Name})
	if err == nil {
		s.Core.Database().NotifyAll()
	}
	return response(&v1.SetDefaultAgentProfileResponse{Revision: uint32(value.Revision)}, err)
}
func (s *Server) ListDrivers(ctx context.Context, req *connect.Request[v1.ListDriversRequest]) (*connect.Response[v1.ListDriversResponse], error) {
	out := &v1.ListDriversResponse{Drivers: []*v1.Driver{}}
	names := []string{}
	for name := range s.Config.Drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		defaults, _ := structpb.NewStruct(s.Config.Drivers[name].Defaults)
		out.Drivers = append(out.Drivers, &v1.Driver{Name: name, Label: name, Defaults: defaults})
	}
	return response(out, nil)
}
