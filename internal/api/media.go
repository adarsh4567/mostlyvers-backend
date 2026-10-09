package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func validImageType(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}

func (s *Server) storeImage(ctx context.Context, folder, filename, contentType string, body io.Reader, size int64) (string, error) {
	if s.media.Configured() {
		return s.media.Upload(ctx, "mostlyvers/"+folder, filename, body)
	}
	key := path.Join(folder, store.NewID(), filename)
	return key, s.objects.Put(ctx, s.objects.PublicBucket, key, contentType, body, size)
}

func (s *Server) adminMediaUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		httpx.WriteError(w, r, httpx.NewError(413, "FILE_TOO_LARGE", "Images must be 5 MB or smaller."))
		return
	}
	kind := strings.ToUpper(r.FormValue("kind"))
	folders := map[string]string{"BOOK_COVER": "book-covers", "AUTHOR_PHOTO": "author", "OWNER_PHOTO": "owners", "PROFILE_IMAGE": "profiles"}
	folder, ok := folders[kind]
	if !ok {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_KIND_INVALID", "Upload kind is not supported."))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil || header.Size < 1 || header.Size > 5<<20 || !validImageType(header.Header.Get("Content-Type")) {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_INVALID", "Use a JPG, PNG or WebP image no larger than 5 MB."))
		return
	}
	defer file.Close()
	digest := sha256.New()
	reference, err := s.storeImage(r.Context(), folder, header.Filename, header.Header.Get("Content-Type"), io.TeeReader(file, digest), header.Size)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id := store.NewID()
	provider := "R2"
	if s.media.Configured() {
		provider = "CLOUDINARY"
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO uploads(id,owner_id,kind,file_name,content_type,size_bytes,checksum_sha256,bucket,object_key,provider_upload_id,status,expires_at,completed_at,provider) VALUES($1,$2,$3,$4,$5,$6,$7,'media',$8,NULL,'VALID',$9,$9,$10)`, id, principal(r).AccountID, kind, header.Filename, header.Header.Get("Content-Type"), header.Size, hex.EncodeToString(digest.Sum(nil)), reference, time.Now().Add(24*time.Hour), provider)
	if err != nil {
		s.deleteImage(r.Context(), reference)
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 201, map[string]any{"uploadRef": id, "status": "VALID", "url": s.imageURL(reference)})
}
