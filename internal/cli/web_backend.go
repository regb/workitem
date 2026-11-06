package cli

import (
	"bytes"
	"context"
	"io"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/itemnotes"
	webaccess "github.com/regb/workitem/internal/web"
)

// webBackend only translates browser requests and presentation data. Every
// domain operation runs in the daemon through generated clients.
type webBackend struct{ cfg Config }

func webRequest[T any](ctx context.Context, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	client := webaccess.ClientID(ctx)
	if client == "" {
		client = "web"
	}
	req.Header().Set("Wi-Client-Id", client)
	req.Header().Set("Wi-Client-Kind", "web")
	if user := webaccess.User(ctx); user != "" {
		req.Header().Set("Wi-Client-Label", user)
	}
	return req
}
func (b webBackend) CheckItem(ctx context.Context, id string) error {
	_, err := b.cfg.API.Items.GetItem(ctx, webRequest(ctx, &v1.GetItemRequest{ItemId: id}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return webaccess.ErrNotFound
	}
	return err
}
func (b webBackend) Attachment(ctx context.Context, id, digest string) (io.ReadCloser, itemnotes.Attachment, error) {
	stream, err := b.cfg.API.Items.ReadAttachment(ctx, webRequest(ctx, &v1.ReadAttachmentRequest{ItemId: id, Sha256: digest}))
	if err != nil {
		return nil, itemnotes.Attachment{}, err
	}
	defer stream.Close()
	var data bytes.Buffer
	meta := itemnotes.Attachment{}
	for stream.Receive() {
		v := stream.Msg()
		if v.Attachment != nil {
			meta = apiview.Attachment(v.Attachment)
		}
		data.Write(v.Chunk)
	}
	if stream.Err() != nil {
		return nil, meta, stream.Err()
	}
	return io.NopCloser(bytes.NewReader(data.Bytes())), meta, nil
}
