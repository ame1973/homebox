package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"homebox/internal/mediaimage"
)

const mediaUploadBodyLimit = mediaimage.MaxInputBytes + (64 << 10)

// readMediaUpload preserves the raw file and normalized declared MIME. It does
// not authorize, decode, sanitize, stage or publish an image. A future handler
// must hold bounded admission for the entire read and processing lifecycle.
func readMediaUpload(w http.ResponseWriter, r *http.Request, requestID string) ([]byte, string, bool) {
	unsupported := func() ([]byte, string, bool) {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Send one unencoded JPEG, PNG or WebP multipart file.", requestID)
		return nil, "", false
	}
	invalid := func() ([]byte, string, bool) {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Send exactly one nonempty file field and no other fields.", requestID)
		return nil, "", false
	}
	failed := func(err error) ([]byte, string, bool) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "The upload exceeds the allowed size.", requestID)
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "The multipart request could not be read.", requestID)
		}
		return nil, "", false
	}
	if err := r.Context().Err(); err != nil {
		return failed(err)
	}
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		return unsupported()
	}
	kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/form-data" || len(params) != 1 || params["boundary"] == "" {
		return unsupported()
	}
	if r.ContentLength > mediaUploadBodyLimit {
		return failed(&http.MaxBytesError{Limit: mediaUploadBodyLimit})
	}
	if r.Body == nil {
		return invalid()
	}
	body := uploadContextReader{ctx: r.Context(), reader: http.MaxBytesReader(w, r.Body, mediaUploadBodyLimit)}
	parts := multipart.NewReader(body, params["boundary"])
	part, err := parts.NextRawPart()
	if errors.Is(err, io.EOF) {
		return invalid()
	}
	if err != nil {
		return failed(err)
	}
	dispositions := part.Header.Values("Content-Disposition")
	if len(dispositions) == 0 {
		return invalid()
	}
	if len(dispositions) != 1 {
		return failed(errors.New("ambiguous multipart disposition"))
	}
	disposition, fields, err := mime.ParseMediaType(dispositions[0])
	if err != nil {
		return failed(err)
	}
	if disposition != "form-data" || fields["name"] != "file" {
		return invalid()
	}
	if len(part.Header.Values("Content-Type")) != 1 || len(part.Header.Values("Content-Encoding")) != 0 || len(part.Header.Values("Content-Transfer-Encoding")) != 0 {
		return unsupported()
	}
	kind, params, err = mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || len(params) != 0 || kind != "image/jpeg" && kind != "image/png" && kind != "image/webp" {
		return unsupported()
	}
	file, err := io.ReadAll(io.LimitReader(part, mediaimage.MaxInputBytes+1))
	if err != nil {
		return failed(err)
	}
	if len(file) > mediaimage.MaxInputBytes {
		return failed(&http.MaxBytesError{Limit: mediaimage.MaxInputBytes})
	}
	if len(file) == 0 {
		return invalid()
	}
	if _, err = parts.NextRawPart(); err == nil {
		return invalid()
	} else if !errors.Is(err, io.EOF) {
		return failed(err)
	}
	// The multipart reader may buffer past its closing boundary; all remaining
	// transport bytes still count towards the total body ceiling.
	if _, err = io.Copy(io.Discard, body); err != nil {
		return failed(err)
	}
	if err = r.Context().Err(); err != nil {
		return failed(err)
	}
	return file, kind, true
}

type uploadContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader uploadContextReader) Read(p []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(p)
}
