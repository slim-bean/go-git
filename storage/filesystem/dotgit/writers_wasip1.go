//go:build wasip1

package dotgit

// WASM/OPFS override for newPackWrite.
//
// The default PackWriter opens the same temp file twice and uses a goroutine
// with syncedReader to parse the pack index concurrently while data is
// written. This doesn't work on OPFS (SyncAccessHandle is exclusive, seek
// is broken, and the async fd_write fallback has its own quirks with large
// streaming writes).
//
// This build-tagged override buffers all pack data in memory, parses from
// the memory buffer, then writes the final .pack file in a single bulk
// write. This avoids ALL OPFS streaming write issues. Acceptable for
// shallow clones (1-5 MB packs).

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/hash"

	"github.com/go-git/go-billy/v5"
)

type PackWriter struct {
	Notify func(plumbing.Hash, *idxfile.Writer)

	fs       billy.Filesystem
	buf      bytes.Buffer
	checksum plumbing.Hash
	writer   *idxfile.Writer
}

func newPackWrite(fs billy.Filesystem) (*PackWriter, error) {
	return &PackWriter{fs: fs}, nil
}

func (w *PackWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

func (w *PackWriter) Close() error {
	defer func() {
		if w.Notify != nil && w.writer != nil && w.writer.Finished() {
			w.Notify(w.checksum, w.writer)
		}
	}()

	packData := w.buf.Bytes()
	if len(packData) == 0 {
		return nil
	}

	// Parse the pack from the in-memory buffer.
	w.writer = new(idxfile.Writer)
	s := packfile.NewScanner(bytes.NewReader(packData))
	parser, err := packfile.NewParser(s, w.writer)
	if err != nil {
		if err == packfile.ErrEmptyPackfile {
			return nil
		}
		return err
	}

	checksum, err := parser.Parse()
	if err != nil {
		return err
	}
	w.checksum = checksum

	if w.writer == nil || !w.writer.Finished() {
		return nil
	}

	return w.save(packData)
}

func (w *PackWriter) save(packData []byte) error {
	base := w.fs.Join(objectsPath, packPath, fmt.Sprintf("pack-%s", w.checksum))

	// Write the index file.
	idx, err := w.fs.Create(fmt.Sprintf("%s.idx", base))
	if err != nil {
		return err
	}
	if err := w.encodeIdx(idx); err != nil {
		_ = idx.Close()
		return err
	}
	if err := idx.Close(); err != nil {
		return err
	}
	fixPermissions(w.fs, fmt.Sprintf("%s.idx", base))

	// Write the pack file in a single bulk write from the memory buffer.
	packPath := fmt.Sprintf("%s.pack", base)
	pf, err := w.fs.Create(packPath)
	if err != nil {
		return err
	}
	if _, err := pf.Write(packData); err != nil {
		_ = pf.Close()
		return err
	}
	if err := pf.Close(); err != nil {
		return err
	}
	fixPermissions(w.fs, packPath)

	return nil
}

func (w *PackWriter) encodeIdx(wr io.Writer) error {
	idx, err := w.writer.Index()
	if err != nil {
		return err
	}
	e := idxfile.NewEncoder(wr)
	_, err = e.Encode(idx)
	return err
}

// ObjectWriter writes and stores loose objects.
type ObjectWriter struct {
	objfile.Writer
	fs billy.Filesystem
	f  billy.File
}

func newObjectWriter(fs billy.Filesystem) (*ObjectWriter, error) {
	f, err := fs.TempFile(fs.Join(objectsPath, packPath), "tmp_obj_")
	if err != nil {
		return nil, err
	}

	return &ObjectWriter{
		Writer: (*objfile.NewWriter(f)),
		fs:     fs,
		f:      f,
	}, nil
}

func (w *ObjectWriter) Close() error {
	if err := w.Writer.Close(); err != nil {
		return err
	}

	if err := w.f.Close(); err != nil {
		return err
	}

	return w.save()
}

func (w *ObjectWriter) save() error {
	hex := w.Hash().String()
	file := w.fs.Join(objectsPath, hex[0:2], hex[2:hash.HexSize])

	if _, err := w.fs.Stat(file); err == nil || os.IsExist(err) {
		return w.fs.Remove(w.f.Name())
	}

	if err := w.fs.Rename(w.f.Name(), file); err != nil {
		return err
	}
	fixPermissions(w.fs, file)

	return nil
}
