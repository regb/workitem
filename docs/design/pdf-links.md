# PDF links

Wi does not embed a PDF renderer. PDF attachments and qualified workspace PDF links open ordinary browser tabs with `target="_blank"` and `rel="noopener noreferrer"`. The file panel also offers Open PDF in new tab and Download PDF instead of loading PDF bytes into the source editor.

## Saved results

Use `wi attach --item ITEM report.pdf` to share a generated document, including one built outside the checkout. The original stays untouched. The attachment is an immutable snapshot addressed by its digest and remains available after checkout release. Replacing the same attachment name updates its chat link to the new digest.

PDF responses use `application/pdf` and `Content-Disposition: inline`. `?download=1` requests attachment disposition. Byte ranges are supported for browser PDF readers. HTML, SVG, and files falsely labeled as PDFs still download rather than execute as Wi's origin.

## Files in the checkout

`WorkspaceService.GetWorkspaceDocument` reads a bounded PDF snapshot through the daemon. It requires the current workspace claim ID and generation, permits only regular `.pdf` files with a PDF signature, and rejects traversal, Git metadata, symlinks, special files, and documents over 16 MiB. Browser access goes through the web adapter and generated API client, never direct adapter filesystem reads.

The item-scoped web URL includes the claim tokens. It does not retarget to a reused checkout slot. Workspace responses are not cached because a rebuild may replace the file; attachment responses are private and immutable. Both endpoints support ranges and explicit downloads. Files outside the checkout must be attached, not exposed through arbitrary host paths.

No builds run on opening a PDF link. Build commands remain an agent or operator action.
