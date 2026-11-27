package server

// MCP app catalog, documents and files: documents, files, office.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) documents() {
	b.read("documents", "list", "Search document metadata and OCR matches.", "/documents", "q created_from created_to correspondent document_type sort limit offset")
	b.read("documents", "get", "Read document metadata.", "/documents/{id}", "")
	b.read("documents", "text", "Read extracted document text; large documents may exceed the response limit.", "/documents/{id}/text", "").TextResponse = true
	b.read("documents", "notes", "Read annotations on a document.", "/documents/{id}/notes", "")
	b.write("documents", "annotate", "Add an annotation to a document.", "POST", "/documents/{id}/notes", textFields("text"), "text")
	b.write("documents", "update", "Update the document title or creation date, preserving other fields.", "PATCH", "/documents/{id}", textFields("title created"), "")
}

func (b *mcpCatalogBuilder) files() {
	b.read("files", "spaces", "List file spaces available to you.", "/spaces", "")
	b.read("files", "list", "List files in an accessible space and relative path.", "/fs/list", "space path")
	b.read("files", "stat", "Read file metadata.", "/fs/stat", "space path")
	b.read("files", "search", "Search your accessible files.", "/fs/search", "space path q")
	b.read("files", "read_text", "Read a UTF-8 text file (up to 512 KiB); binary files are not supported.", "/fs/raw", "space path").TextResponse = true
	b.write("files", "mkdir", "Create a folder within an accessible file space.", "POST", "/fs/mkdir", textFields("space path"), "space path")
	b.write("files", "save_text", "Create or replace a text file's complete contents. Read and preserve existing contents when needed.", "POST", "/fs/text", textFields("space path content"), "space path content")
	// The bytes never pass through the tool call: an app build, a photo or a video can't fit in JSON. Files hands out a link that
	// writes only this path, once, within an hour, as the person; the agent then PUTs the file there. A change (always asked):
	// what gets written is only known when it arrives.
	b.write("files", "upload_link", "Get a one-time link to upload one file of any type or size (an app build, a photo, a PDF, a video) to path in an accessible space, then send the file's bytes to the returned url with a PUT, e.g. curl -T <file> '<url>'. The link writes only that path, works once and expires after an hour. overwrite = true replaces a file already there; otherwise the new file is kept beside it under a new name. Create the folder first with pocket_files_mkdir.", "POST", "/fs/upload-link", textFields("space path").booleans("overwrite"), "space path")
}

func (b *mcpCatalogBuilder) office() {
	b.read("office", "sources", "List document sources available to you; credentials are omitted.", "/sources", "")
	b.read("office", "list", "List documents in an accessible source folder.", "/sources/{source_id}/ls", "path")
	b.read("office", "read_text", "Read bounded text from DOCX, XLSX, PPTX, OpenDocument or UTF-8 files. Images, formatting and live formulas are not interpreted.", "/sources/{source_id}/text", "path")
	b.read("office", "recent", "List recently opened office documents.", "/recent", "")
	b.write("office", "create", "Create a blank office document, spreadsheet or presentation.", "POST", "/sources/{source_id}/new", textFields("path name").enum("type", "docx", "xlsx", "pptx"), "name type")
	b.write("office", "rename", "Rename a document within its existing folder.", "POST", "/sources/{source_id}/rename", textFields("from name"), "from name")
}
