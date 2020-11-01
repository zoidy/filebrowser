package http

import (
	"errors"
	"net/http"
	"net/url"
	gopath "path"
	"path/filepath"
	"strings"
	"fmt"
	"io"
	"time"
	"crypto/rand"
	"encoding/base64"

	"github.com/mholt/archiver"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/filebrowser/filebrowser/v2/share"
)

func slashClean(name string) string {
	if name == "" || name[0] != '/' {
		name = "/" + name
	}
	return gopath.Clean(name)
}

func parseQueryFiles(r *http.Request, f *files.FileInfo, _ *users.User) ([]string, error) {
	var fileSlice []string
	names := strings.Split(r.URL.Query().Get("files"), ",")

	if len(names) == 0 {
		fileSlice = append(fileSlice, f.Path)
	} else {
		for _, name := range names {
			name, err := url.QueryUnescape(strings.Replace(name, "+", "%2B", -1)) //nolint:shadow
			if err != nil {
				return nil, err
			}

			name = slashClean(name)
			fileSlice = append(fileSlice, filepath.Join(f.Path, name))
		}
	}

	return fileSlice, nil
}

//nolint: goconst
func parseQueryAlgorithm(r *http.Request) (string, archiver.Writer, error) {
	// TODO: use enum
	switch r.URL.Query().Get("algo") {
	case "zip", "true", "":
		return ".zip", archiver.NewZip(), nil
	case "tar":
		return ".tar", archiver.NewTar(), nil
	case "targz":
		return ".tar.gz", archiver.NewTarGz(), nil
	case "tarbz2":
		return ".tar.bz2", archiver.NewTarBz2(), nil
	case "tarxz":
		return ".tar.xz", archiver.NewTarXz(), nil
	case "tarlz4":
		return ".tar.lz4", archiver.NewTarLz4(), nil
	case "tarsz":
		return ".tar.sz", archiver.NewTarSz(), nil
	default:
		return "", nil, errors.New("format not implemented")
	}
}

func setContentDisposition(w http.ResponseWriter, r *http.Request, file *files.FileInfo) {
	if r.URL.Query().Get("inline") == "true" {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		// As per RFC6266 section 4.3
		w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(file.Name))
	}
}

var rawHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.user.Perm.Download {
		return http.StatusAccepted, nil
	}

	file, err := files.NewFileInfo(files.FileOptions{
		Fs:      d.user.Fs,
		Path:    r.URL.Path,
		Modify:  d.user.Perm.Modify,
		Expand:  false,
		Checker: d,
	})
	if err != nil {
		return errToStatus(err), err
	}

	if !file.IsDir {
		return rawFileHandler(w, r, file)
	}

	return rawDirHandler(w, r, d, file)
})

func addFile(ar archiver.Writer, d *data, path, commonPath string) error {
	// Checks are always done with paths with "/" as path separator.
	path = strings.Replace(path, "\\", "/", -1)
	if !d.Check(path) {
		return nil
	}

	info, err := d.user.Fs.Stat(path)
	if err != nil {
		return err
	}

	file, err := d.user.Fs.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	filename := strings.TrimPrefix(path, commonPath)
	filename = strings.TrimPrefix(filename, "/")
	err = ar.Write(archiver.File{
		FileInfo: archiver.FileInfo{
			FileInfo:   info,
			CustomName: filename,
		},
		ReadCloser: file,
	})
	if err != nil {
		return err
	}

	if info.IsDir() {
		names, err := file.Readdirnames(0)
		if err != nil {
			return err
		}

		for _, name := range names {
			err = addFile(ar, d, filepath.Join(path, name), commonPath)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func rawDirHandler(w http.ResponseWriter, r *http.Request, d *data, file *files.FileInfo) (int, error) {
	if r.URL.Query().Get("algo") == "m3u" {
		return rawPlaylistHandler(w, r, d, file)
	}
	filenames, err := parseQueryFiles(r, file, d.user)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	extension, ar, err := parseQueryAlgorithm(r)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	name := file.Name
	if name == "." || name == "" {
		name = "archive"
	}
	name += extension
	w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))

	err = ar.Create(w)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer ar.Close()

	commonDir := fileutils.CommonPrefix('/', filenames...)

	for _, fname := range filenames {
		err = addFile(ar, d, fname, commonDir)
		if err != nil {
			return http.StatusInternalServerError, err
		}
	}

	return 0, nil
}

func rawFileHandler(w http.ResponseWriter, r *http.Request, file *files.FileInfo) (int, error) {
	fd, err := file.Fs.Open(file.Path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer fd.Close()

	setContentDisposition(w, r, file)

	http.ServeContent(w, r, file.Name, file.ModTime, fd)
	return 0, nil
}

func rawPlaylistHandler(w http.ResponseWriter, r *http.Request, d *data, file *files.FileInfo) (int, error) {
	filenames, err := parseQueryFiles(r, file, d.user)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	name := file.Name
	if name == "." || name == "" {
		name = "playlist"
	}
	name += ".m3u"
	w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))

    _, err = fmt.Fprintf(w, "#EXTM3U\n")
	if err != nil {
		return http.StatusInternalServerError, err
	}

	for _, fname := range filenames {
		err = addPlaylist(w, r, d, fname)
		if err != nil {
			return http.StatusInternalServerError, err
		}
	}

	return 0, nil
}

func addPlaylist(ar io.Writer, r *http.Request, d *data, path string) error {
	// Checks are always done with paths with "/" as path separator.
	path = strings.Replace(path, "\\", "/", -1)
	if !d.Check(path) {
		return nil
	}

	info, err := d.user.Fs.Stat(path)
	if err != nil {
		return err
	}

    file, err := d.user.Fs.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if err != nil {
		return err
	}

	if info.IsDir() {
		names, err := file.Readdirnames(0)
		if err != nil {
			return err
		}

		for _, name := range names {
			err = addPlaylist(ar, r, d, filepath.Join(path, name))
			if err != nil {
				return err
			}
		}
	} else {
		// Generate 24h shared links for included files
		var s *share.Link

		bytes := make([]byte, 6)
		_, err := rand.Read(bytes)
		if err != nil {
			return err
		}

		str := base64.URLEncoding.EncodeToString(bytes)

		var expire int64 = 0
		var add time.Duration
		add = time.Hour * time.Duration(24)
		expire = time.Now().Add(add).Unix()

		var fo *files.FileOptions
		fo = &files.FileOptions {
			Fs: d.user.Fs,
			Path: path,
			Modify: false,
			Expand:true,
			Checker: d,
		}
		fi,err := files.NewFileInfo(*fo)
		if err != nil {
			return err
		}
		if fi.Type!="video" && fi.Type!="audio" {
			return nil
		}

		s = &share.Link {
			Path:   file.Name(),
			Hash:   str,
			Expire: expire,
			UserID: d.user.ID,
		}

		if err := d.store.Share.Save(s); err != nil {
			return err
		}

		var baseURL = ""
		baseURL = r.URL.Query().Get("base")

		str = fmt.Sprintf("%s/api/public/dl/%s/%s\n", baseURL, s.Hash, filepath.Base(s.Path))
		_, err = fmt.Fprintf(ar, str)
	}

	return nil
}
