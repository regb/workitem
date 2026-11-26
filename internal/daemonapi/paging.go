package daemonapi

import (
	"encoding/base64"
	"strconv"
)

// maxPage bounds a page of the lists that grow with an item's life: notes,
// jobs, review revisions and threads.
const maxPage = 500

// page is the part of a list of total entries a request asks for: from its
// page_token's offset, at most size entries, or limit when size is zero or
// larger; a limit of zero leaves the page unbounded. next is the token for
// the rest, empty on the last page. Tokens are offsets: best-effort under
// concurrent changes, so a later page may shift.
func page(token string, size uint32, total, limit int) (start, end int, next string, err error) {
	if token != "" {
		data, decodeErr := base64.RawURLEncoding.DecodeString(token)
		if decodeErr != nil {
			return 0, 0, "", invalid("invalid page_token")
		}
		start, err = strconv.Atoi(string(data))
		if err != nil || start < 0 || start > total {
			return 0, 0, "", invalid("invalid page_token")
		}
	}
	count := int(size)
	if limit > 0 && (count == 0 || count > limit) {
		count = limit
	}
	end = total
	if count > 0 && total-start > count {
		end = start + count
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return start, end, next, nil
}
