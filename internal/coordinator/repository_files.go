package coordinator

import (
	"context"
	"encoding/json"

	"github.com/regb/workitem/internal/repofiles"
)

const (
	MethodRepositoryFiles      = "repository_files"
	MethodRepositoryFileRead   = "repository_file_read"
	MethodRepositoryFilePut    = "repository_file_put"
	MethodRepositoryFileRemove = "repository_file_remove"
)

// RepositoryFileRequest names a registered repository, and for a single
// file its path in a checkout.
type RepositoryFileRequest struct {
	Selector string `json:"selector"`
	Path     string `json:"path,omitempty"`
	Content  []byte `json:"content,omitempty"`
}

type RepositoryFilesResult struct {
	RepositoryID string           `json:"repository_id"`
	Files        []repofiles.File `json:"files"`
}

type RepositoryFileContent struct {
	File    repofiles.File `json:"file"`
	Content []byte         `json:"content"`
}

func (c *Client) RepositoryFiles(ctx context.Context, selector string) (RepositoryFilesResult, error) {
	var result RepositoryFilesResult
	err := c.Call(ctx, MethodRepositoryFiles, RepositoryFileRequest{Selector: selector}, &result)
	return result, err
}

func (c *Client) ReadRepositoryFile(ctx context.Context, selector, path string) (RepositoryFileContent, error) {
	var result RepositoryFileContent
	err := c.Call(ctx, MethodRepositoryFileRead, RepositoryFileRequest{Selector: selector, Path: path}, &result)
	return result, err
}

func (c *Client) PutRepositoryFile(ctx context.Context, selector, path string, content []byte) (repofiles.File, error) {
	var result repofiles.File
	err := c.Call(ctx, MethodRepositoryFilePut, RepositoryFileRequest{Selector: selector, Path: path, Content: content}, &result)
	return result, err
}

func (c *Client) RemoveRepositoryFile(ctx context.Context, selector, path string) error {
	return c.Call(ctx, MethodRepositoryFileRemove, RepositoryFileRequest{Selector: selector, Path: path}, nil)
}

// repositoryFile serves the repository files methods. Content is written
// only here, under the domain lock; agents have no access.
func (s *Server) repositoryFile(method string, payload json.RawMessage) (any, error) {
	var request RepositoryFileRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	repository, err := s.database.RepositorySettings(request.Selector)
	if err != nil {
		return nil, err
	}
	dir := s.native.RepositoryFilesDir(repository.Repository.ID)
	switch method {
	case MethodRepositoryFiles:
		files, err := repofiles.List(dir)
		return RepositoryFilesResult{RepositoryID: repository.Repository.ID, Files: files}, err
	case MethodRepositoryFileRead:
		content, err := repofiles.Read(dir, request.Path)
		if err != nil {
			return nil, err
		}
		return RepositoryFileContent{File: repofiles.File{Path: request.Path, Size: int64(len(content)), SHA256: repofiles.Digest(content)}, Content: content}, nil
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	if method == MethodRepositoryFilePut {
		return repofiles.Put(dir, request.Path, request.Content)
	}
	return struct{}{}, repofiles.Remove(dir, request.Path)
}
