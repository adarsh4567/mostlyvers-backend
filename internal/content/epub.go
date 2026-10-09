package content

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/net/html"
)

const maxUncompressedEPUB = int64(400 << 20)

type Resource struct {
	ID, Href, MediaType string
	Data                []byte
	SpinePosition       *int
	Checksum            string
}
type EPUB struct {
	Sanitized []byte
	Resources []Resource
}
type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}
type packageXML struct {
	Manifest []struct {
		ID        string `xml:"id,attr"`
		Href      string `xml:"href,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

func ProcessEPUB(data []byte) (EPUB, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return EPUB{}, errors.New("EPUB_ZIP_INVALID")
	}
	files := map[string]*zip.File{}
	var total int64
	for _, f := range reader.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || name == ".." {
			return EPUB{}, errors.New("EPUB_PATH_TRAVERSAL")
		}
		if int64(f.UncompressedSize64) > maxUncompressedEPUB {
			return EPUB{}, errors.New("EPUB_TOO_LARGE")
		}
		total += int64(f.UncompressedSize64)
		if total > maxUncompressedEPUB {
			return EPUB{}, errors.New("EPUB_TOO_LARGE")
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > 100 {
			return EPUB{}, errors.New("EPUB_COMPRESSION_RATIO")
		}
		files[name] = f
	}
	mime, ok := files["mimetype"]
	if !ok {
		return EPUB{}, errors.New("EPUB_MIMETYPE_MISSING")
	}
	mimeData, err := readZip(mime, 128)
	if err != nil || strings.TrimSpace(string(mimeData)) != "application/epub+zip" {
		return EPUB{}, errors.New("EPUB_MIMETYPE_INVALID")
	}
	containerFile, ok := files["META-INF/container.xml"]
	if !ok {
		return EPUB{}, errors.New("EPUB_CONTAINER_MISSING")
	}
	raw, err := readZip(containerFile, 1<<20)
	if err != nil {
		return EPUB{}, err
	}
	var container containerXML
	if xml.Unmarshal(raw, &container) != nil || len(container.Rootfiles) == 0 {
		return EPUB{}, errors.New("EPUB_CONTAINER_INVALID")
	}
	opfPath := path.Clean(container.Rootfiles[0].FullPath)
	opfFile, ok := files[opfPath]
	if !ok {
		return EPUB{}, errors.New("EPUB_PACKAGE_MISSING")
	}
	raw, err = readZip(opfFile, 5<<20)
	if err != nil {
		return EPUB{}, err
	}
	var pkg packageXML
	if xml.Unmarshal(raw, &pkg) != nil || len(pkg.Manifest) == 0 || len(pkg.Spine) == 0 {
		return EPUB{}, errors.New("EPUB_PACKAGE_INVALID")
	}
	base := path.Dir(opfPath)
	mediaByPath := map[string]string{}
	for _, item := range pkg.Manifest {
		mediaByPath[path.Clean(path.Join(base, item.Href))] = item.MediaType
	}
	spine := map[string]int{}
	for i, item := range pkg.Spine {
		spine[item.IDRef] = i
	}
	replacements := map[string][]byte{}
	resources := make([]Resource, 0, len(pkg.Manifest))
	for _, item := range pkg.Manifest {
		href := path.Clean(path.Join(base, item.Href))
		if strings.HasPrefix(href, "../") || strings.Contains(item.Href, "://") {
			return EPUB{}, errors.New("EPUB_REMOTE_RESOURCE")
		}
		file, ok := files[href]
		if !ok {
			return EPUB{}, fmt.Errorf("EPUB_RESOURCE_MISSING: %s", item.Href)
		}
		body, err := readZip(file, maxUncompressedEPUB)
		if err != nil {
			return EPUB{}, err
		}
		if isDocument(item.MediaType) {
			body, err = sanitizeHTML(body, href, files, mediaByPath)
			if err != nil {
				return EPUB{}, err
			}
			replacements[href] = body
		}
		sum := sha256.Sum256(body)
		resource := Resource{ID: item.ID, Href: item.Href, MediaType: item.MediaType, Data: body, Checksum: fmt.Sprintf("%x", sum[:])}
		if pos, ok := spine[item.ID]; ok {
			resource.SpinePosition = &pos
		}
		resources = append(resources, resource)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		header := file.FileHeader
		header.Name = path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		entry, err := writer.CreateHeader(&header)
		if err != nil {
			return EPUB{}, err
		}
		body, ok := replacements[header.Name]
		if !ok {
			body, err = readZip(file, maxUncompressedEPUB)
			if err != nil {
				return EPUB{}, err
			}
		}
		if _, err = entry.Write(body); err != nil {
			return EPUB{}, err
		}
	}
	if err = writer.Close(); err != nil {
		return EPUB{}, err
	}
	return EPUB{Sanitized: output.Bytes(), Resources: resources}, nil
}

func readZip(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, limit+1))
}
func isDocument(media string) bool {
	return media == "application/xhtml+xml" || media == "text/html" || media == "image/svg+xml"
}
func sanitizeHTML(source []byte, documentPath string, files map[string]*zip.File, mediaByPath map[string]string) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(source))
	if err != nil {
		return nil, errors.New("EPUB_XHTML_INVALID")
	}
	remoteResource := false
	var clean func(*html.Node)
	clean = func(node *html.Node) {
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.ElementNode && (child.Data == "script" || child.Data == "iframe" || child.Data == "object" || child.Data == "embed" || child.Data == "form") {
				node.RemoveChild(child)
			} else {
				attrs := child.Attr[:0]
				for _, attr := range child.Attr {
					name := strings.ToLower(attr.Key)
					value := strings.TrimSpace(strings.ToLower(attr.Val))
					if name == "href" && child.Data == "a" && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
						// Preserve the book's visible link text without allowing the
						// protected reader to navigate to an untrusted remote origin.
						continue
					}
					if (name == "src" || name == "href") && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
						remoteResource = true
						continue
					}
					if name == "style" && (strings.Contains(value, "http://") || strings.Contains(value, "https://")) {
						remoteResource = true
						continue
					}
					if strings.HasPrefix(name, "on") || name == "srcdoc" || value == "javascript:" || strings.HasPrefix(value, "javascript:") {
						continue
					}
					if (name == "src" || name == "href") && (child.Data == "img" || child.Data == "image" || child.Data == "source") && !strings.HasPrefix(value, "data:") && !strings.HasPrefix(value, "#") {
						resolved := path.Clean(path.Join(path.Dir(documentPath), attr.Val))
						file, exists := files[resolved]
						media := mediaByPath[resolved]
						if !exists || !strings.HasPrefix(media, "image/") {
							continue
						}
						imageData, readErr := readZip(file, 25<<20)
						if readErr != nil {
							continue
						}
						attr.Val = "data:" + media + ";base64," + base64.StdEncoding.EncodeToString(imageData)
					}
					attrs = append(attrs, attr)
				}
				child.Attr = attrs
				if child.Type == html.ElementNode && child.Data == "style" && child.FirstChild != nil {
					css := strings.ToLower(child.FirstChild.Data)
					if strings.Contains(css, "http://") || strings.Contains(css, "https://") {
						remoteResource = true
						node.RemoveChild(child)
						child = next
						continue
					}
				}
				clean(child)
			}
			child = next
		}
	}
	clean(doc)
	if remoteResource {
		return nil, errors.New("EPUB_REMOTE_RESOURCE")
	}
	var out bytes.Buffer
	if err = html.Render(&out, doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
