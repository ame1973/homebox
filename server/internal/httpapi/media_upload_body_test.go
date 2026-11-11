package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"homebox/internal/mediaimage"
)

const uploadTestBoundary = "homebox-upload-boundary"

type uploadTestPart struct {
	disposition []string
	contentType []string
	transfer    string
	encoding    string
	body        []byte
}

func uploadPart(name, filename, contentType string, body []byte) uploadTestPart {
	disposition := fmt.Sprintf(`form-data; name=%q`, name)
	if filename != "" {
		disposition += fmt.Sprintf(`; filename=%q`, filename)
	}
	return uploadTestPart{disposition: []string{disposition}, contentType: []string{contentType}, body: body}
}

func uploadRequest(t *testing.T, parts ...uploadTestPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(uploadTestBoundary); err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		header := textproto.MIMEHeader{}
		if part.disposition != nil {
			header["Content-Disposition"] = part.disposition
		}
		if part.contentType != nil {
			header["Content-Type"] = part.contentType
		}
		if part.transfer != "" {
			header.Set("Content-Transfer-Encoding", part.transfer)
		}
		if part.encoding != "" {
			header.Set("Content-Encoding", part.encoding)
		}
		destination, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = destination.Write(part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/media", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func uploadRecorder() *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	response.Header().Set("X-Request-ID", "request")
	return response
}

func TestMediaUploadMultipartPreservesRawFileAndNormalizesDeclaredType(t *testing.T) {
	raw := []byte{'\x00', 0xff, '\r', '\n', 'x', '=', '3', 'D'}
	for _, tc := range []struct {
		declared, expected, filename string
	}{
		{"IMAGE/JPEG", "image/jpeg", "../../private/SENSITIVE-GPS.jpg"},
		{"image/PnG", "image/png", "photo; name*=not-a-field.png"},
		{"image/webp", "image/webp", ""},
	} {
		t.Run(tc.expected, func(t *testing.T) {
			request := uploadRequest(t, uploadPart("file", tc.filename, tc.declared, raw))
			response := uploadRecorder()
			file, kind, ok := readMediaUpload(response, request, "request")
			if !ok || kind != tc.expected || !bytes.Equal(file, raw) {
				t.Fatalf("raw file or normalized MIME changed: ok=%v kind=%q status=%d", ok, kind, response.Code)
			}
		})
	}
}

func TestMediaUploadMultipartRequiresSeparateImageValidation(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	jpegBytes := encoded.Bytes()
	for _, tc := range []struct {
		name, declared string
		raw            []byte
		want           error
	}{
		{"valid JPEG", "image/jpeg", jpegBytes, nil},
		{"mismatched MIME", "image/png", jpegBytes, mediaimage.ErrInvalid},
		{"truncated JPEG", "image/jpeg", jpegBytes[:len(jpegBytes)-2], mediaimage.ErrInvalid},
		{"WebP decoder not available", "image/webp", []byte("RIFF-not-a-decoder"), mediaimage.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := uploadRequest(t, uploadPart("file", "ignored", tc.declared, tc.raw))
			response := uploadRecorder()
			file, kind, ok := readMediaUpload(response, request, "request")
			if !ok {
				t.Fatalf("multipart parsing unexpectedly rejected bytes: %d", response.Code)
			}
			var output bytes.Buffer
			result, err := mediaimage.Sanitize(request.Context(), file, kind, &output, mediaimage.Limits{})
			if !errors.Is(err, tc.want) || err == nil && (result.MIMEType != "image/jpeg" || output.Len() == 0) {
				t.Fatalf("separate image validation returned an unexpected result: %v", err)
			}
		})
	}
}

func TestMediaUploadMultipartUsesContractCeilings(t *testing.T) {
	if mediaimage.MaxInputBytes != 33554432 || mediaUploadBodyLimit != 33619968 {
		t.Fatal("runtime multipart limits differ from the upload contract")
	}
}

func TestMediaUploadMultipartRejectsAmbiguousOrUnsupportedRequests(t *testing.T) {
	good := uploadPart("file", "SENSITIVE-GPS.jpg", "image/jpeg", []byte("private raw bytes"))
	for _, tc := range []struct {
		name   string
		parts  []uploadTestPart
		edit   func(*http.Request)
		status int
	}{
		{"no file", nil, nil, http.StatusUnprocessableEntity},
		{"empty file", []uploadTestPart{uploadPart("file", "image.jpg", "image/jpeg", nil)}, nil, http.StatusUnprocessableEntity},
		{"wrong field", []uploadTestPart{uploadPart("owner_id", "image.jpg", "image/jpeg", []byte("x"))}, nil, http.StatusUnprocessableEntity},
		{"extra file", []uploadTestPart{good, good}, nil, http.StatusUnprocessableEntity},
		{"extra owner", []uploadTestPart{good, uploadPart("owner_id", "", "text/plain", []byte("item"))}, nil, http.StatusUnprocessableEntity},
		{"missing disposition", []uploadTestPart{{contentType: []string{"image/jpeg"}, body: []byte("x")}}, nil, http.StatusUnprocessableEntity},
		{"duplicate disposition", []uploadTestPart{{disposition: []string{`form-data; name="file"`, `form-data; name="file"`}, contentType: []string{"image/jpeg"}, body: []byte("x")}}, nil, http.StatusBadRequest},
		{"conflicting name", []uploadTestPart{{disposition: []string{`form-data; name="file"; name="owner"`}, contentType: []string{"image/jpeg"}, body: []byte("x")}}, nil, http.StatusBadRequest},
		{"wrong disposition", []uploadTestPart{{disposition: []string{`attachment; name="file"`}, contentType: []string{"image/jpeg"}, body: []byte("x")}}, nil, http.StatusUnprocessableEntity},
		{"missing part type", []uploadTestPart{{disposition: []string{`form-data; name="file"`}, body: []byte("x")}}, nil, http.StatusUnsupportedMediaType},
		{"duplicate part type", []uploadTestPart{{disposition: []string{`form-data; name="file"`}, contentType: []string{"image/jpeg", "image/jpeg"}, body: []byte("x")}}, nil, http.StatusUnsupportedMediaType},
		{"unsupported part type", []uploadTestPart{uploadPart("file", "image.gif", "image/gif", []byte("x"))}, nil, http.StatusUnsupportedMediaType},
		{"part type parameter", []uploadTestPart{uploadPart("file", "image.png", "image/png; charset=utf-8", []byte("x"))}, nil, http.StatusUnsupportedMediaType},
		{"transfer encoding", []uploadTestPart{{disposition: []string{`form-data; name="file"`}, contentType: []string{"image/jpeg"}, transfer: "quoted-printable", body: []byte("x=0A")}}, nil, http.StatusUnsupportedMediaType},
		{"part encoding", []uploadTestPart{{disposition: []string{`form-data; name="file"`}, contentType: []string{"image/jpeg"}, encoding: "gzip", body: []byte("x")}}, nil, http.StatusUnsupportedMediaType},
		{"missing request type", []uploadTestPart{good}, func(r *http.Request) { r.Header.Del("Content-Type") }, http.StatusUnsupportedMediaType},
		{"duplicate request type", []uploadTestPart{good}, func(r *http.Request) { r.Header.Add("Content-Type", r.Header.Get("Content-Type")) }, http.StatusUnsupportedMediaType},
		{"wrong request type", []uploadTestPart{good}, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, http.StatusUnsupportedMediaType},
		{"missing boundary", []uploadTestPart{good}, func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data") }, http.StatusUnsupportedMediaType},
		{"request encoding", []uploadTestPart{good}, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, http.StatusUnsupportedMediaType},
		{"truncated multipart", []uploadTestPart{good}, func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(uploadTestPrefix + "private raw bytes"))
			r.ContentLength = -1
		}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := uploadRequest(t, tc.parts...)
			if tc.edit != nil {
				tc.edit(request)
			}
			response := uploadRecorder()
			file, kind, ok := readMediaUpload(response, request, "request")
			if ok || file != nil || kind != "" || response.Code != tc.status {
				t.Fatalf("invalid multipart accepted or misclassified: ok=%v kind=%q status=%d", ok, kind, response.Code)
			}
			if strings.Contains(response.Body.String(), "SENSITIVE-GPS") || strings.Contains(response.Body.String(), "private raw bytes") {
				t.Fatal("error exposed a private filename or file content")
			}
		})
	}
}

const uploadTestPrefix = "--" + uploadTestBoundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"image.jpg\"\r\nContent-Type: image/jpeg\r\n\r\n"
const uploadTestSuffix = "\r\n--" + uploadTestBoundary + "--\r\n"

type uploadRepeatedByte struct{ remaining int64 }

func (reader *uploadRepeatedByte) Read(p []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), reader.remaining))
	for i := range p[:n] {
		p[i] = 'x'
	}
	reader.remaining -= int64(n)
	return n, nil
}

func uploadStream(fileBytes, trailingBytes int64) io.Reader {
	return io.MultiReader(strings.NewReader(uploadTestPrefix), &uploadRepeatedByte{fileBytes}, strings.NewReader(uploadTestSuffix), &uploadRepeatedByte{trailingBytes})
}

func uploadStreamRequest(fileBytes, trailingBytes int64) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/media", uploadStream(fileBytes, trailingBytes))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+uploadTestBoundary)
	return request
}

func TestMediaUploadMultipartBoundsFileAndEntireChunkedBody(t *testing.T) {
	trailingAtLimit := int64(mediaUploadBodyLimit - len(uploadTestPrefix) - len(uploadTestSuffix) - 1)
	for _, tc := range []struct {
		name       string
		file, tail int64
		status     int
	}{
		{"file at limit", mediaimage.MaxInputBytes, 0, http.StatusOK},
		{"file over limit", mediaimage.MaxInputBytes + 1, 0, http.StatusRequestEntityTooLarge},
		{"entire body at limit", 1, trailingAtLimit, http.StatusOK},
		{"entire body over limit after closing boundary", 1, trailingAtLimit + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := uploadStreamRequest(tc.file, tc.tail)
			response := uploadRecorder()
			file, kind, ok := readMediaUpload(response, request, "request")
			if tc.status == http.StatusOK {
				if !ok || kind != "image/jpeg" || int64(len(file)) != tc.file || file[0] != 'x' || file[len(file)-1] != 'x' {
					t.Fatalf("valid boundary rejected: ok=%v status=%d bytes=%d", ok, response.Code, len(file))
				}
			} else if ok || file != nil || kind != "" || response.Code != tc.status {
				t.Fatalf("oversized input returned a prefix: ok=%v status=%d bytes=%d", ok, response.Code, len(file))
			}
		})
	}
}

type uploadFailReader struct{}

func (uploadFailReader) Read([]byte) (int, error) {
	return 0, errors.New("private upstream read failure")
}

type uploadCountingReader struct{ reads int }

func (reader *uploadCountingReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, io.EOF
}

type uploadCancelReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (reader uploadCancelReader) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	if n > 0 {
		reader.cancel()
	}
	return n, err
}

func TestMediaUploadMultipartRejectsDeclaredSizeReadFailureAndCancellation(t *testing.T) {
	counter := &uploadCountingReader{}
	request := uploadStreamRequest(1, 0)
	request.Body = io.NopCloser(counter)
	request.ContentLength = mediaUploadBodyLimit + 1
	response := uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusRequestEntityTooLarge || counter.reads != 0 {
		t.Fatal("oversized declared body was read or accepted", response.Code, counter.reads)
	}

	request = uploadStreamRequest(mediaimage.MaxInputBytes+1, 0)
	request.ContentLength = 1
	response = uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("an understated length bypassed the actual file limit", response.Code)
	}

	request = uploadStreamRequest(1, 0)
	request.Body = io.NopCloser(io.MultiReader(strings.NewReader(uploadTestPrefix), uploadFailReader{}))
	response = uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "private upstream") {
		t.Fatal("body read failure was exposed or accepted", response.Code)
	}

	request = uploadStreamRequest(1, 0)
	request.Body = io.NopCloser(io.MultiReader(strings.NewReader(uploadTestPrefix+"x"+uploadTestSuffix), uploadFailReader{}))
	response = uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusBadRequest {
		t.Fatal("a late body failure returned a file prefix", response.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request = uploadStreamRequest(1, 0).WithContext(ctx)
	response = uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusBadRequest {
		t.Fatal("cancelled upload returned file data", response.Code)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	request = uploadStreamRequest(128, 0).WithContext(ctx)
	request.Body = io.NopCloser(uploadCancelReader{reader: request.Body, cancel: cancel})
	response = uploadRecorder()
	if file, kind, ok := readMediaUpload(response, request, "request"); ok || file != nil || kind != "" || response.Code != http.StatusBadRequest {
		t.Fatal("upload cancelled during reading returned file data", response.Code)
	}
}

func TestMediaUploadMultipartReadsActualChunkedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "request")
		file, kind, ok := readMediaUpload(w, r, "request")
		if ok {
			w.Header().Set("X-Test-MIME", kind)
			_, _ = w.Write(file)
		}
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL, uploadStream(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+uploadTestBoundary)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	err = errors.Join(err, response.Body.Close())
	if err != nil || response.StatusCode != http.StatusOK || response.Header.Get("X-Test-MIME") != "image/jpeg" || string(body) != "xxx" {
		t.Fatalf("chunked socket request lost file data: status=%d err=%v", response.StatusCode, err)
	}
}

func FuzzMediaUploadMultipart(f *testing.F) {
	f.Add("multipart/form-data; boundary="+uploadTestBoundary, []byte(uploadTestPrefix+"raw"+uploadTestSuffix))
	f.Add("application/json", []byte(`{"file":"not multipart"}`))
	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/media", bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		response := uploadRecorder()
		file, kind, ok := readMediaUpload(response, request, "request")
		if ok {
			if len(file) == 0 || len(file) > mediaimage.MaxInputBytes || kind != "image/jpeg" && kind != "image/png" && kind != "image/webp" {
				t.Fatal("accepted an invalid raw file representation")
			}
		} else if file != nil || kind != "" || response.Code < 400 {
			t.Fatal("rejected input returned file data or a successful status")
		}
	})
}
