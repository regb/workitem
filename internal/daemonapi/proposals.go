package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// ProposeItem records work an item's conversation found, for its user to
// file. The item agent asking is not waiting on the user: it returns at once,
// and nothing is created until a user files it.
func (s *Server) ProposeItem(ctx context.Context, req *connect.Request[v1.ProposeItemRequest]) (*connect.Response[v1.ProposeItemResponse], error) {
	r := req.Msg
	who := caller(ctx).principal
	if who.item() && who.itemID != r.ItemId {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("an item's agent proposes from its own item"))
	}
	source, err := s.canonical(ctx, r.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	repository := source.Manifest.Repository.ID
	labels, err := model.NormalizeLabels(r.Labels)
	if err != nil {
		return nil, invalid(err.Error())
	}
	title := strings.TrimSpace(r.Title)
	if title == "" || strings.ContainsAny(title, "\r\n") {
		return nil, invalid("a single-line title is required")
	}
	proposalID := id()
	if r.RequestId != "" {
		sum := sha256.Sum256([]byte(r.ItemId + ":" + r.RequestId))
		proposalID = "proposal-" + hex.EncodeToString(sum[:12])
	}
	if len(r.Description) > coordinator.MaxProposalDescription {
		return nil, invalid("the description is too long")
	}
	// The description is written first, so a recorded proposal always has
	// it; a retry of one already recorded keeps what it recorded.
	if _, err := s.Core.Database().GetProposal(proposalID); err != nil {
		if err := writeProposalDescription(s.proposalFile(r.ItemId, proposalID), r.Description); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	saved, created, err := s.Core.Database().SaveProposal(coordinator.Proposal{ID: proposalID, ItemID: r.ItemId, Proposer: model.ActorFrom(ctx), RepositoryID: repository, Title: title, Description: r.Description, Labels: labels, CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, rpcError(err)
	}
	if created {
		_ = s.App.Store.AppendEvent(ctx, r.ItemId, model.NewEventFrom(ctx, saved.CreatedAt, "proposal.created", map[string]any{"proposal_id": saved.ID, "title": saved.Title}))
	}
	s.Core.Database().NotifyItems(r.ItemId)
	return response(&v1.ProposeItemResponse{Proposal: s.proposal(saved)}, nil)
}

func (s *Server) ListProposals(ctx context.Context, req *connect.Request[v1.ListProposalsRequest]) (*connect.Response[v1.ListProposalsResponse], error) {
	values, err := s.Core.Database().ListProposals(req.Msg.ItemId, req.Msg.PendingOnly)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.ListProposalsResponse{Proposals: []*v1.Proposal{}}
	items := []string{}
	for _, value := range values {
		items = append(items, value.ItemID)
	}
	viewable := map[string]bool{}
	for _, id := range s.viewable(ctx, items) {
		viewable[id] = true
	}
	for _, value := range values {
		if !viewable[value.ItemID] {
			continue
		}
		out.Proposals = append(out.Proposals, s.proposal(value))
	}
	return response(out, nil)
}

// FileProposal creates the proposed item as the user filing it: theirs, not
// the proposing agent's, with the proposal as its cause.
func (s *Server) FileProposal(ctx context.Context, req *connect.Request[v1.FileProposalRequest]) (*connect.Response[v1.FileProposalResponse], error) {
	r := req.Msg
	who := caller(ctx).principal
	if who.item() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only a person files a proposal"))
	}
	value, err := s.Core.Database().GetProposal(r.ProposalId)
	if err != nil {
		return nil, rpcError(err)
	}
	if value.State == coordinator.ProposalDismissed {
		return nil, precondition("the proposal was dismissed")
	}
	title, description, labels := value.Title, s.proposalDescription(value), value.Labels
	if strings.TrimSpace(r.Title) != "" {
		title = strings.TrimSpace(r.Title)
	}
	if r.Description != "" {
		description = r.Description
	}
	if r.ReplaceLabels {
		labels = r.Labels
	}
	filing := model.WithOrigin(ctx, model.Origin{Actor: who.String(), Cause: "proposal:" + value.ID})
	created, err := s.CreateItem(filing, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: value.RepositoryID, Title: title, Description: description, Labels: labels, RequestId: "proposal:" + value.ID}))
	if err != nil {
		return nil, err
	}
	decided, err := s.Core.Database().DecideProposal(value.ID, coordinator.ProposalFiled, created.Msg.Item.Id, who.String(), "", time.Now().UTC())
	if err != nil {
		return nil, partial(err, created.Msg.Item.Id, "created item")
	}
	_ = s.App.Store.AppendEvent(filing, value.ItemID, model.NewEventFrom(filing, *decided.DecidedAt, "proposal.filed", map[string]any{"proposal_id": value.ID, "item_id": created.Msg.Item.Id}))
	s.Core.Database().NotifyItems(value.ItemID, created.Msg.Item.Id)
	return response(&v1.FileProposalResponse{Proposal: s.proposal(decided), Item: created.Msg.Item}, nil)
}

func (s *Server) DismissProposal(ctx context.Context, req *connect.Request[v1.DismissProposalRequest]) (*connect.Response[v1.DismissProposalResponse], error) {
	who := caller(ctx).principal
	if who.item() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only a person dismisses a proposal"))
	}
	reason := strings.TrimSpace(req.Msg.Reason)
	if len(reason) > 2048 {
		return nil, invalid("the reason is too long")
	}
	decided, err := s.Core.Database().DecideProposal(req.Msg.ProposalId, coordinator.ProposalDismissed, "", who.String(), reason, time.Now().UTC())
	if err != nil {
		return nil, rpcError(err)
	}
	_ = s.App.Store.AppendEvent(ctx, decided.ItemID, model.NewEventFrom(ctx, *decided.DecidedAt, "proposal.dismissed", map[string]any{"proposal_id": decided.ID}))
	s.Core.Database().NotifyItems(decided.ItemID)
	return response(&v1.DismissProposalResponse{Proposal: s.proposal(decided)}, nil)
}

// proposalFile is where a proposal's description is kept: with the item
// that proposed it.
func (s *Server) proposalFile(itemID, proposalID string) string {
	return filepath.Join(s.App.Store.ItemDir(itemID), "proposals", proposalID+".md")
}

func writeProposalDescription(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".proposal-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(text); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (s *Server) proposalDescription(value coordinator.Proposal) string {
	data, err := os.ReadFile(s.proposalFile(value.ItemID, value.ID))
	if err != nil {
		return ""
	}
	return string(data)
}

// proposal is the API form of a proposal, with its description read from
// its file.
func (s *Server) proposal(value coordinator.Proposal) *v1.Proposal {
	value.Description = s.proposalDescription(value)
	return proposalView(value)
}

func proposalView(value coordinator.Proposal) *v1.Proposal {
	state := map[string]v1.ProposalState{coordinator.ProposalPending: v1.ProposalState_PROPOSAL_STATE_PENDING, coordinator.ProposalFiled: v1.ProposalState_PROPOSAL_STATE_FILED, coordinator.ProposalDismissed: v1.ProposalState_PROPOSAL_STATE_DISMISSED}[value.State]
	return &v1.Proposal{Id: value.ID, ItemId: value.ItemID, Proposer: value.Proposer, RepositoryId: value.RepositoryID, Title: value.Title, Description: value.Description, Labels: value.Labels, State: state, CreatedAt: stamp(value.CreatedAt), FiledItemId: value.FiledItemID, DecidedBy: value.DecidedBy, DecidedAt: optionalStamp(value.DecidedAt), Reason: value.Reason}
}
