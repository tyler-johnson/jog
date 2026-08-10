package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractBinary pulls the jog binary (member) out of the release archive
// at src and writes it, executable, to dst. The format follows the asset
// name — .zip or .tar.gz — never the OS, so a future format change fails
// loudly in pickAsset's tests instead of silently here.
func extractBinary(src, assetName, member, dst string) error {
	var found io.ReadCloser
	var err error
	if strings.HasSuffix(assetName, ".zip") {
		found, err = openZipMember(src, member)
	} else {
		found, err = openTarMember(src, member)
	}
	if err != nil {
		return err
	}
	defer found.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, found); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("extracting %s: %w", member, err)
	}
	return out.Close()
}

func openTarMember(src, member string) (io.ReadCloser, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("reading the release archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("reading the release archive: %w", err)
		}
		if filepath.Base(hdr.Name) == member && hdr.Typeflag == tar.TypeReg {
			return readCloser{tr, f}, nil
		}
	}
	f.Close()
	return nil, fmt.Errorf("the release archive has no %s member", member)
}

func openZipMember(src, member string) (io.ReadCloser, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("reading the release archive: %w", err)
	}
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) == member && !zf.FileInfo().IsDir() {
			rc, err := zf.Open()
			if err != nil {
				zr.Close()
				return nil, err
			}
			return readCloser{rc, zr}, nil
		}
	}
	zr.Close()
	return nil, fmt.Errorf("the release archive has no %s member", member)
}

// readCloser pairs a member reader with the archive handle that owns it.
type readCloser struct {
	io.Reader
	closer io.Closer
}

func (r readCloser) Close() error { return r.closer.Close() }
